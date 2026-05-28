// Package longmem is the AP-D1 long-term per-user memory store.
// The chat handler writes facts the LLM extracts at end-of-turn
// (ai.ExtractSalientFacts) and reads them back at the start of
// the next turn via Recall, prepending them to the system prompt.
//
// Recall comes in two flavours: recency- + salience-weighted (Recall)
// and embedding-similarity ANN (RecallSemantic). The embedding column is
// NOT NULL with a zero-vector placeholder when no real embedding is
// available; its width is whatever migrate.EnsureUserMemoryEmbedding set
// it to (the active embedder dim), discovered at runtime rather than
// hardcoded — see embeddingDim.
//
// Privacy contract: every memory row is FK'd to users(id) ON DELETE
// CASCADE — deleting a user evicts their entire memory. The GDPR
// "My Memory" drawer (per-row + bulk delete + JSON export) ships via
// the internal/memory handler package + Profile.tsx, backed by
// /api/user/memory. The embedding column is widened to the active
// embedder dim at startup (migrate.EnsureUserMemoryEmbedding); the
// admin re-embed-user-memory job repopulates existing rows after a
// change.
package longmem

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// dimMismatchWarnLogged keeps Insert's "dim mismatch → zero-vector
// fallback" warning to one emission per process lifetime — a chat
// turn with 5 facts would otherwise log five times per turn.
var dimMismatchWarnLogged atomic.Bool

// Memory is one row of user_memory exposed to read callers. The
// embedding column from the table is intentionally NOT in this
// shape — the recall path doesn't need it for v1, and exposing
// it would force every read site to handle pgvector's float32
// slice marshalling.
type Memory struct {
	ID             int64
	UserID         string
	KbID           string // empty when the memory is global to the user
	Kind           string // "episodic" | "semantic" | "procedural"
	Content        string
	Salience       float64
	CreatedAt      time.Time
	LastAccessedAt time.Time
	AccessCount    int
}

// Store is the longmem persistence surface. Production wires
// *PgStore (pgxpool-backed); tests inject a fake.
//
// Insert accepts an optional embedding []float64. When nil or empty,
// the zero-vector placeholder is stored (v1 back-compat). When the
// chat_longmem_recall_semantic flag is on, callers should embed the
// fact content (typically via ai.EncodeQuery using the same model
// used for chunk search) and pass it here so RecallSemantic can
// later return it as a near-neighbor of the user's current query.
//
// RecallSemantic does two-stage retrieval: HNSW ANN candidates by
// cosine distance, then re-ranks by (semantic_similarity ×
// recency × salience × access). queryEmbedding empty/nil yields
// an empty result + nil error so the chat layer can fall back to
// the recency-only Recall without an extra round-trip.
type Store interface {
	Insert(ctx context.Context, userID, kbID, kind, content string, salience float64, embedding []float64) error
	Recall(ctx context.Context, userID, kbID string, topK int, decayDays int) ([]Memory, error)
	RecallSemantic(ctx context.Context, userID, kbID string, queryEmbedding []float64, topK int, decayDays int) ([]Memory, error)
	// NearestMemories returns the user's k semantically nearest
	// memories to queryEmbedding (no recency / salience weighting),
	// scoped to a single KB or, when kbID is empty, including global
	// memories. Returns nil + nil error when queryEmbedding is
	// empty so callers can short-circuit cleanly. Powers the T1-3
	// conflict-detection candidate pool.
	NearestMemories(ctx context.Context, userID, kbID string, queryEmbedding []float64, topK int) ([]Memory, error)
	// InsertWithSupersede atomically inserts a new memory row AND
	// marks the listed existing memory rows as superseded by the
	// freshly-inserted one. supersedeIDs may be empty; the call is
	// then equivalent to Insert. Powers the T1-3 Mem0
	// {supersede, create_new} action with the same write semantics
	// as plain Insert (zero-vector fallback on dim mismatch, etc.).
	InsertWithSupersede(ctx context.Context, userID, kbID, kind, content string, salience float64, embedding []float64, supersedeIDs []int64) error
	DeleteByUser(ctx context.Context, userID string) error
}

// PgStore is the pgxpool-backed implementation. The DB pool MUST
// point at the main DB where migration 0045 lives.
type PgStore struct {
	pool *pgxpool.Pool

	// dimMu guards the lazily-resolved embedding column dimension.
	// The column width is set at startup by
	// migrate.EnsureUserMemoryEmbedding to the active embedder's dim;
	// the store discovers it on first write rather than hardcoding a
	// value, so a re-dimensioned column self-heals after a restart.
	dimMu       sync.Mutex
	resolvedDim int // 0 = not yet resolved
}

// fallbackEmbeddingDim is used only when the column dimension cannot be
// read (e.g. table missing during a half-migrated boot). Matches the
// migration 0045 initial width so the zero placeholder still parses.
const fallbackEmbeddingDim = 1536

// zeroVectorLiteral renders the pgvector text literal for a dim-length
// zero vector, e.g. "[0,0,0]".
func zeroVectorLiteral(dim int) string {
	if dim < 1 {
		dim = fallbackEmbeddingDim
	}
	var b strings.Builder
	b.Grow(dim*2 + 2)
	b.WriteByte('[')
	for i := 0; i < dim; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('0')
	}
	b.WriteByte(']')
	return b.String()
}

// embeddingDim returns the user_memory.embedding column width, reading it
// once from the catalog and caching the result. The catalog query runs
// OUTSIDE the lock (double-checked) so concurrent first-callers don't
// serialize on a DB round-trip; a redundant query under a startup race is
// harmless (the read is idempotent). Failure falls back to
// fallbackEmbeddingDim and is not cached, so a transient error doesn't
// permanently poison the value.
func (s *PgStore) embeddingDim(ctx context.Context) int {
	s.dimMu.Lock()
	if s.resolvedDim != 0 {
		d := s.resolvedDim
		s.dimMu.Unlock()
		return d
	}
	s.dimMu.Unlock()

	dim := s.queryEmbeddingDim(ctx) // 0 on error/unexpected type

	s.dimMu.Lock()
	if s.resolvedDim == 0 && dim > 0 {
		s.resolvedDim = dim
	}
	d := s.resolvedDim
	s.dimMu.Unlock()
	if d == 0 {
		return fallbackEmbeddingDim
	}
	return d
}

// queryEmbeddingDim reads the embedding column width from the catalog.
// format_type renders the vector typmod as "vector(N)"; we parse N.
// Returns 0 on any error or unexpected type so the caller falls back
// (and does not cache the failure).
func (s *PgStore) queryEmbeddingDim(ctx context.Context) int {
	var ft string
	err := s.pool.QueryRow(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod)
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'user_memory'
		   AND a.attname = 'embedding'
		   AND NOT a.attisdropped
	`).Scan(&ft)
	if err != nil {
		logctx.From(ctx).Warn("longmem: could not read embedding column dim; using fallback",
			"err", err, "fallback", fallbackEmbeddingDim)
		return 0
	}
	var dim int
	if _, perr := fmt.Sscanf(ft, "vector(%d)", &dim); perr != nil || dim < 1 {
		logctx.From(ctx).Warn("longmem: unexpected embedding column type; using fallback",
			"format_type", ft, "fallback", fallbackEmbeddingDim)
		return 0
	}
	return dim
}

// NewPgStore wraps a pool. nil-pool callers get an error from
// every method — the chat handler nil-checks and falls back to
// no-memory mode.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

// embFormatBufPool recycles the format buffer used by formatEmbedding so
// back-to-back memory inserts/recalls reuse the same backing storage rather
// than allocating ~65 KB per 4096-dim embedding. Mirrors the vector
// package's embeddingFormatBufPool; duplicated here (not imported) to keep
// longmem free of an import cycle on the vector package. Holds *[]byte so
// Get/Put skip the boxing alloc a value-type slice header would incur.
// 72 KB initial covers the largest production embedding dimension (4096 ×
// ~16 bytes per float + brackets ≈ 65.5 KB).
var embFormatBufPool = sync.Pool{
	New: func() any { b := make([]byte, 0, 72*1024); return &b },
}

// formatEmbedding renders a []float64 as the pgvector text literal
// "[v0,v1,…,vN]" without allocating through json.Marshal. Mirrors the
// vector package's formatEmbedding helper — kept local to avoid an
// import cycle (longmem must not depend on the vector package).
//
// bitSize=32 in AppendFloat matches the vector package: every embedding
// provider in this codebase returns 32-bit floats over the wire, so
// emitting at float32 precision is round-trip lossless and skips the
// expensive shortest-round-trip search FormatFloat runs at full float64.
func formatEmbedding(emb []float64) string {
	if len(emb) == 0 {
		return ""
	}
	bufPtr := embFormatBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	if cap(buf) < len(emb)*16+2 {
		buf = make([]byte, 0, len(emb)*16+2)
	}
	buf = append(buf, '[')
	for i, v := range emb {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, v, 'g', -1, 32)
	}
	buf = append(buf, ']')
	out := string(buf)
	*bufPtr = buf
	embFormatBufPool.Put(bufPtr)
	return out
}

// Insert persists one extracted fact. kbID may be empty (the
// memory is global across the user's KBs). On constraint
// violation (e.g. unknown kind) the error is logged and dropped
// upstream — write path is fire-and-forget, never fails the
// chat turn.
//
// embedding is optional: when nil/empty, the zero-vector placeholder
// is written (v1 back-compat). When the chat layer's
// chat_longmem_recall_semantic flag is on, callers should encode
// content and pass the resulting vector here so RecallSemantic
// finds the row by similarity in subsequent turns.
//
// When len(embedding) doesn't match the actual column dimension
// (discovered lazily via pg_attribute), Insert silently falls back to
// the zero-vector placeholder and logs a one-shot warning. After an
// embedder change, run the re-embed-user-memory maintenance job to
// backfill stored facts with the new dimension.
func (s *PgStore) Insert(ctx context.Context, userID, kbID, kind, content string, salience float64, embedding []float64) error {
	if s == nil || s.pool == nil {
		return errors.New("longmem: no DB pool configured")
	}
	if userID == "" || kind == "" || strings.TrimSpace(content) == "" {
		return errors.New("longmem: required fields missing")
	}
	if salience < 0 {
		salience = 0
	}
	if salience > 1 {
		salience = 1
	}

	// kb_id NULL when the caller passed empty — global-to-user
	// memories don't pin to a single KB. The COALESCE-NULL trick
	// keeps the SQL parameterised without conditional construction.
	var kbArg any
	if kbID != "" {
		kbArg = kbID
	}

	colDim := s.embeddingDim(ctx)
	embLiteral := zeroVectorLiteral(colDim)
	if len(embedding) > 0 {
		if len(embedding) == colDim {
			embLiteral = formatEmbedding(embedding)
		} else if !dimMismatchWarnLogged.Swap(true) {
			logctx.From(ctx).Warn("longmem: embedding dim mismatch; storing zero-vector placeholder",
				"got_dim", len(embedding),
				"expected_dim", colDim,
				"hint", "run the re-embed-user-memory maintenance job after an embedder change")
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_memory (user_id, kb_id, kind, content, embedding, salience)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5::vector, $6)
	`, userID, kbArg, kind, content, embLiteral, salience)
	if err != nil {
		observability.RecordLongmemWrite("error")
		return fmt.Errorf("longmem: insert: %w", err)
	}
	observability.RecordLongmemWrite("create")
	return nil
}

// Recall returns the most relevant memories for (userID, kbID),
// ordered by a recency- and salience-weighted score. v1 doesn't
// use the embedding column.
//
// Score = salience × exp(-Δt_days / decayDays) × log1p(access_count)
//
// Δt_days is measured from `last_accessed_at` so memories that
// the recall path "touched" recently (and bumped access_count)
// stay relevant longer than untouched stale entries — Mem0's
// observation that recall itself signals durability.
//
// kbID can be empty: in that case both kb-scoped and global
// memories of the user are eligible. Global memories (kb_id
// IS NULL) are always eligible regardless of the kbID filter
// because they describe the user, not the KB.
//
// topK is clamped to [1, 20]; beyond 20 the per-turn token cost
// of injecting memories outweighs the recall lift. decayDays is
// clamped to [1, 365] (operators almost never want shorter than
// a day or longer than a year).
func (s *PgStore) Recall(ctx context.Context, userID, kbID string, topK, decayDays int) ([]Memory, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("longmem: no DB pool configured")
	}
	if userID == "" {
		return nil, errors.New("longmem: user_id required")
	}
	if topK < 1 {
		topK = 1
	}
	if topK > 20 {
		topK = 20
	}
	if decayDays < 1 {
		decayDays = 1
	}
	if decayDays > 365 {
		decayDays = 365
	}

	// The score formula lives in SQL because it's cheap to
	// evaluate per-row in the planner and avoids fetching every
	// row of the user just to rank in Go. ln(1 + access_count)
	// uses the natural log so a memory accessed 5 times scores
	// ~1.79× a never-accessed one (vs 5× under linear weighting,
	// which would dominate the salience signal).
	const recallQuery = `
		SELECT id, user_id::text, COALESCE(kb_id::text, ''), kind, content, salience,
		       created_at, last_accessed_at, access_count
		  FROM user_memory
		 WHERE user_id = $1::uuid
		   AND superseded_by IS NULL
		   AND ($2::uuid IS NULL OR kb_id IS NULL OR kb_id = $2::uuid)
		 ORDER BY (
		     salience
		     * EXP(- EXTRACT(EPOCH FROM (NOW() - last_accessed_at)) / 86400.0 / $3::float)
		     * (1.0 + LN(1.0 + access_count))
		 ) DESC
		 LIMIT $4
	`
	var kbArg any
	if kbID != "" {
		kbArg = kbID
	}
	rows, err := s.pool.Query(ctx, recallQuery, userID, kbArg, decayDays, topK)
	if err != nil {
		return nil, fmt.Errorf("longmem: recall query: %w", err)
	}
	defer rows.Close()

	out := make([]Memory, 0, topK)
	ids := make([]int64, 0, topK)
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.UserID, &m.KbID, &m.Kind, &m.Content,
			&m.Salience, &m.CreatedAt, &m.LastAccessedAt, &m.AccessCount); err != nil {
			return nil, fmt.Errorf("longmem: scan: %w", err)
		}
		out = append(out, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("longmem: rows: %w", err)
	}

	// Bump access stats for the recalled rows. Non-fatal: a
	// failed bump just means the memories age normally next
	// time. Fire-and-forget would be cleaner but we want the
	// row state consistent with what the caller saw, so we run
	// it inline and fold an error into a warning.
	if len(ids) > 0 {
		_, uerr := s.pool.Exec(ctx, `
			UPDATE user_memory
			   SET last_accessed_at = NOW(),
			       access_count = access_count + 1
			 WHERE id = ANY($1::bigint[])
		`, ids)
		if uerr != nil {
			// Non-fatal: the recalled rows still age normally on next
			// recall. Surface as a warning so persistent failures (schema
			// drift, connection loss) are visible without breaking the
			// caller's recall result.
			logctx.From(ctx).Warn("longmem.recall.access_update_failed",
				"err", uerr,
				"recalled_ids", len(ids),
			)
		}
	}

	observability.RecordLongmemRecall(len(out))
	return out, nil
}

// recallSemanticCandidatePool is the HNSW ANN over-fetch limit used
// by RecallSemantic. Pulled from the index by cosine distance, then
// re-ranked in SQL by the combined semantic × recency × salience
// score. Tighter than the chunks pipeline's k×5 over-fetch because
// per-user memory rows number in the hundreds at most; pulling 50
// candidates is enough for a topK ≤ 20 final cut without spending
// the memory budget on irrelevant neighbours.
const recallSemanticCandidatePool = 50

// RecallSemantic does ANN cosine recall against the per-user memory
// embedding column, re-ranked by the v1 (recency × salience × access)
// score so a stale-but-on-topic memory still surfaces ahead of a
// fresh-but-off-topic one. queryEmbedding empty/nil short-circuits
// with no rows and no error — callers should fall back to Recall
// (recency-only) under that signal.
//
// SQL injection: queryEmbedding flows through pgvector's text
// literal format ("[v0,v1,…]") via a single bound parameter; the
// CTE structure is static. user_id / kb_id are uuid-bound.
//
// Dim mismatch: when queryEmbedding's length differs from the column
// width (resolved dynamically — see embeddingDim), pgvector raises a
// "different vector dimensions" error, which we surface as the returned
// error so the caller can fall back to Recall. It also signals that the
// re-embed-user-memory job should run after an embedder change.
func (s *PgStore) RecallSemantic(ctx context.Context, userID, kbID string, queryEmbedding []float64, topK, decayDays int) ([]Memory, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("longmem: no DB pool configured")
	}
	if userID == "" {
		return nil, errors.New("longmem: user_id required")
	}
	if len(queryEmbedding) == 0 {
		// Caller asked for semantic recall without an embedding — that's
		// either a config bug or a chat turn where the embedder failed.
		// Return empty so the chat path can fall back to Recall without
		// polluting Prometheus with an "error" outcome.
		return nil, nil
	}
	if topK < 1 {
		topK = 1
	}
	if topK > 20 {
		topK = 20
	}
	if decayDays < 1 {
		decayDays = 1
	}
	if decayDays > 365 {
		decayDays = 365
	}

	// Two-stage SQL. The CTE uses the HNSW index (ORDER BY embedding
	// <=> $5::vector with a LIMIT) to pull the candidate pool; the
	// outer re-rank multiplies (1 - cosine_distance) — i.e. cosine
	// similarity in [-1, 1] — by the v1 score so an irrelevant
	// memory's recency boost can't pull it ahead of a clearly
	// relevant one. We don't subtract or normalise: the v1 score is
	// strictly positive (salience ∈ [0,1] × exp(-Δt) ∈ (0,1] ×
	// log1p_access ≥ 1), so a 0-or-negative semantic similarity
	// pushes the combined value down without inverting the sign.
	const recallSemanticQuery = `
		WITH candidates AS (
			SELECT id, user_id, kb_id, kind, content, salience,
			       created_at, last_accessed_at, access_count,
			       embedding <=> $5::vector AS cos_distance
			  FROM user_memory
			 WHERE user_id = $1::uuid
			   AND superseded_by IS NULL
			   AND ($2::uuid IS NULL OR kb_id IS NULL OR kb_id = $2::uuid)
			 ORDER BY embedding <=> $5::vector
			 LIMIT $6
		)
		SELECT id, user_id::text, COALESCE(kb_id::text, ''), kind, content, salience,
		       created_at, last_accessed_at, access_count
		  FROM candidates
		 ORDER BY (
		     GREATEST(1.0 - cos_distance, 0.0)
		     * salience
		     * EXP(- EXTRACT(EPOCH FROM (NOW() - last_accessed_at)) / 86400.0 / $3::float)
		     * (1.0 + LN(1.0 + access_count))
		 ) DESC
		 LIMIT $4
	`
	var kbArg any
	if kbID != "" {
		kbArg = kbID
	}
	rows, err := s.pool.Query(ctx, recallSemanticQuery,
		userID, kbArg, decayDays, topK,
		formatEmbedding(queryEmbedding), recallSemanticCandidatePool,
	)
	if err != nil {
		return nil, fmt.Errorf("longmem: recall_semantic query: %w", err)
	}
	defer rows.Close()

	out := make([]Memory, 0, topK)
	ids := make([]int64, 0, topK)
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.UserID, &m.KbID, &m.Kind, &m.Content,
			&m.Salience, &m.CreatedAt, &m.LastAccessedAt, &m.AccessCount); err != nil {
			return nil, fmt.Errorf("longmem: scan: %w", err)
		}
		out = append(out, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("longmem: rows: %w", err)
	}

	// Same access-stats bump as v1 Recall — semantic hits should also
	// register so the recency component reflects the fact that this
	// memory was just used. Non-fatal failure: a stuck UPDATE doesn't
	// affect the returned rows.
	if len(ids) > 0 {
		_, uerr := s.pool.Exec(ctx, `
			UPDATE user_memory
			   SET last_accessed_at = NOW(),
			       access_count = access_count + 1
			 WHERE id = ANY($1::bigint[])
		`, ids)
		if uerr != nil {
			logctx.From(ctx).Warn("longmem.recall_semantic.access_update_failed",
				"err", uerr,
				"recalled_ids", len(ids),
			)
		}
	}

	observability.RecordLongmemRecall(len(out))
	return out, nil
}

// NearestMemories fetches the user's k semantically nearest memories
// to queryEmbedding via the HNSW index. No recency or salience
// re-rank; results are pure cosine-distance order so the conflict
// classifier sees the true closest matches (a recency tilt would
// occasionally hide a recently-stored contradicting fact and let a
// stale older "supersede" target win the candidate slot).
//
// Empty queryEmbedding short-circuits with (nil, nil) — same
// contract as RecallSemantic so a missing embedder fails open
// rather than poisoning the conflict-resolution path with empty
// candidates that the LLM would mis-classify as create_new.
func (s *PgStore) NearestMemories(ctx context.Context, userID, kbID string, queryEmbedding []float64, topK int) ([]Memory, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("longmem: no DB pool configured")
	}
	if userID == "" {
		return nil, errors.New("longmem: user_id required")
	}
	if len(queryEmbedding) == 0 {
		return nil, nil
	}
	if topK < 1 {
		topK = 1
	}
	if topK > 20 {
		topK = 20
	}

	const nearestQuery = `
		SELECT id, user_id::text, COALESCE(kb_id::text, ''), kind, content, salience,
		       created_at, last_accessed_at, access_count
		  FROM user_memory
		 WHERE user_id = $1::uuid
		   AND superseded_by IS NULL
		   AND ($2::uuid IS NULL OR kb_id IS NULL OR kb_id = $2::uuid)
		 ORDER BY embedding <=> $3::vector
		 LIMIT $4
	`
	var kbArg any
	if kbID != "" {
		kbArg = kbID
	}
	rows, err := s.pool.Query(ctx, nearestQuery, userID, kbArg, formatEmbedding(queryEmbedding), topK)
	if err != nil {
		return nil, fmt.Errorf("longmem: nearest query: %w", err)
	}
	defer rows.Close()

	out := make([]Memory, 0, topK)
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.UserID, &m.KbID, &m.Kind, &m.Content,
			&m.Salience, &m.CreatedAt, &m.LastAccessedAt, &m.AccessCount); err != nil {
			return nil, fmt.Errorf("longmem: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("longmem: rows: %w", err)
	}
	return out, nil
}

// InsertWithSupersede atomically writes the new memory row and flips
// superseded_by on every row whose id appears in supersedeIDs to
// point at the new row. The two writes share one transaction so a
// crash mid-call cannot leave the new fact stored without retiring
// the old one (which would produce contradictory rows the recall
// path could surface together).
//
// supersedeIDs empty is allowed and produces plain Insert
// semantics — the only DB cost is the BEGIN/COMMIT pair, which the
// chat-layer caller avoids by routing through Insert directly when
// the conflict classifier returns create_new.
//
// Scoping: the UPDATE constrains by user_id so a malicious / buggy
// caller can't supersede another user's memories even with a valid
// row id from this user's record.
func (s *PgStore) InsertWithSupersede(ctx context.Context, userID, kbID, kind, content string, salience float64, embedding []float64, supersedeIDs []int64) error {
	if s == nil || s.pool == nil {
		return errors.New("longmem: no DB pool configured")
	}
	if userID == "" || kind == "" || strings.TrimSpace(content) == "" {
		return errors.New("longmem: required fields missing")
	}
	if salience < 0 {
		salience = 0
	}
	if salience > 1 {
		salience = 1
	}

	colDim := s.embeddingDim(ctx)
	embLiteral := zeroVectorLiteral(colDim)
	if len(embedding) > 0 {
		if len(embedding) == colDim {
			embLiteral = formatEmbedding(embedding)
		} else if !dimMismatchWarnLogged.Swap(true) {
			logctx.From(ctx).Warn("longmem: embedding dim mismatch; storing zero-vector placeholder",
				"got_dim", len(embedding),
				"expected_dim", colDim,
				"hint", "run the re-embed-user-memory maintenance job after an embedder change")
		}
	}

	var kbArg any
	if kbID != "" {
		kbArg = kbID
	}

	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var newID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO user_memory (user_id, kb_id, kind, content, embedding, salience)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5::vector, $6)
			RETURNING id
		`, userID, kbArg, kind, content, embLiteral, salience).Scan(&newID); err != nil {
			return fmt.Errorf("longmem: insert: %w", err)
		}

		if len(supersedeIDs) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE user_memory
				   SET superseded_by = $1::bigint
				 WHERE id = ANY($2::bigint[])
				   AND user_id = $3::uuid
				   AND superseded_by IS NULL
			`, newID, supersedeIDs, userID); err != nil {
				return fmt.Errorf("longmem: supersede update: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		observability.RecordLongmemWrite("error")
		return err
	}
	if len(supersedeIDs) > 0 {
		observability.RecordLongmemWrite("supersede")
	} else {
		observability.RecordLongmemWrite("create")
	}
	return nil
}

// DeleteByUser removes every memory row for the user. Used by the
// account-deletion cascade and by a future DSGVO bulk-clear UI.
// The migration's FK ON DELETE CASCADE handles the same effect
// when users.id is dropped, but the explicit method exists for
// "user wants their memory cleared without deleting their account".
func (s *PgStore) DeleteByUser(ctx context.Context, userID string) error {
	if s == nil || s.pool == nil {
		return errors.New("longmem: no DB pool configured")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM user_memory WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return fmt.Errorf("longmem: delete by user: %w", err)
	}
	return nil
}

// ListByUser returns the user's live (non-superseded) memories newest
// first. Powers the GDPR drawer's list view, the JSON export, and the
// re-embed backfill job's row source.
func (s *PgStore) ListByUser(ctx context.Context, userID string) ([]Memory, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("longmem: no DB pool configured")
	}
	if userID == "" {
		return nil, errors.New("longmem: user_id required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id::text, COALESCE(kb_id::text, ''), kind, content, salience,
		       created_at, last_accessed_at, access_count
		  FROM user_memory
		 WHERE user_id = $1::uuid
		   AND superseded_by IS NULL
		 ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("longmem: list by user: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.UserID, &m.KbID, &m.Kind, &m.Content,
			&m.Salience, &m.CreatedAt, &m.LastAccessedAt, &m.AccessCount); err != nil {
			return nil, fmt.Errorf("longmem: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteByID removes one memory row, scoped to its owner so a user can
// only delete their own rows even if they guess another row id.
func (s *PgStore) DeleteByID(ctx context.Context, userID string, id int64) error {
	if s == nil || s.pool == nil {
		return errors.New("longmem: no DB pool configured")
	}
	if userID == "" {
		return errors.New("longmem: user_id required")
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_memory WHERE id = $1::bigint AND user_id = $2::uuid`, id, userID)
	if err != nil {
		return fmt.Errorf("longmem: delete by id: %w", err)
	}
	return nil
}

// UpdateEmbedding overwrites one row's embedding vector, scoped to its
// owner. Used by the re-embed-user-memory backfill job after a dim change.
func (s *PgStore) UpdateEmbedding(ctx context.Context, userID string, id int64, embedding []float64) error {
	if s == nil || s.pool == nil {
		return errors.New("longmem: no DB pool configured")
	}
	if userID == "" || len(embedding) == 0 {
		return errors.New("longmem: user_id and non-empty embedding required")
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE user_memory SET embedding = $1::vector WHERE id = $2::bigint AND user_id = $3::uuid`,
		formatEmbedding(embedding), id, userID)
	if err != nil {
		return fmt.Errorf("longmem: update embedding: %w", err)
	}
	return nil
}

// ListUserIDsWithMemory returns the distinct user_ids that have at least
// one live memory row. Drives the admin re-embed fan-out (one job per user).
func (s *PgStore) ListUserIDsWithMemory(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("longmem: no DB pool configured")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT user_id::text FROM user_memory WHERE superseded_by IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("longmem: list user ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("longmem: scan user id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FormatMemoriesForPrompt renders a list of recalled memories as a
// system-prompt block. Empty or nil input yields the empty string
// so the chat handler can `prefix + kbSystemPrompt` without
// special-casing absence.
//
// The block is intentionally compact — one line per fact, kind
// label first so the LLM can weight by category. Identity facts
// (semantic) get more weight in the LLM's attention than episodic
// "user uploaded X today" via that ordering.
func FormatMemoriesForPrompt(memories []Memory) string {
	if len(memories) == 0 {
		return ""
	}
	// Stable ordering: semantic first (durable identity), then
	// procedural (workflow), then episodic (recent events). Within
	// each kind, recall-order (the SQL ORDER BY) is preserved.
	var sem, proc, epi []Memory
	for _, m := range memories {
		switch m.Kind {
		case "semantic":
			sem = append(sem, m)
		case "procedural":
			proc = append(proc, m)
		case "episodic":
			epi = append(epi, m)
		}
	}

	var b strings.Builder
	b.WriteString("Conversation memory (durable facts about this user — apply where relevant):\n")
	for _, m := range sem {
		fmt.Fprintf(&b, "- [identity] %s\n", m.Content)
	}
	for _, m := range proc {
		fmt.Fprintf(&b, "- [workflow] %s\n", m.Content)
	}
	for _, m := range epi {
		fmt.Fprintf(&b, "- [recent] %s\n", m.Content)
	}
	return b.String()
}

// score is a Go-side recomputation of the recall ranking score,
// exposed for tests + admin diagnostics. The SQL ORDER BY is the
// authoritative path — this function exists so tests can pin the
// formula's behaviour without round-tripping through pgxpool.
//
// Returned value is unitless; only the relative ordering matters.
func score(salience float64, lastAccessed time.Time, accessCount int, decayDays int, now time.Time) float64 {
	if decayDays < 1 {
		decayDays = 1
	}
	dtDays := now.Sub(lastAccessed).Hours() / 24.0
	decay := math.Exp(-dtDays / float64(decayDays))
	// 1 + log1p(N): never-accessed (N=0) → 1.0; once-accessed
	// (N=1) → 1 + ln(2) ≈ 1.69; mirrors the SQL formula exactly.
	weight := 1.0 + math.Log1p(float64(accessCount))
	return salience * decay * weight
}

// Compile-time interface assertion: the pgxpool-backed PgStore
// must continue to satisfy Store as the interface evolves.
var _ Store = (*PgStore)(nil)
