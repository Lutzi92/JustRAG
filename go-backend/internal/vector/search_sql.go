package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/justrag/go-backend/internal/observability"
)

// excludeCommunitySummaryClause keeps KG community summaries out of the normal
// retrieval pool. Community summaries (node_kind='community_summary') are only
// surfaced by the community-primed global-search path (which injects them by id
// via GraphChunkIDs, bypassing this WHERE clause). When an explicit
// nodeKindFilter is set (eval ablation, or the community retrieval itself), the
// caller's node_kind = $N clause already scopes the query, so no extra exclusion.
func excludeCommunitySummaryClause(nodeKindFilter string) string {
	if nodeKindFilter == "" {
		return " AND node_kind <> 'community_summary'"
	}
	return ""
}

// parenJoin returns "(" + strings.Join(parts, sep) + ")" with a single
// allocation. Pure-string concatenation of that pattern emits two: one for
// strings.Join's result and one for the wrapping concat. On the keyword-search
// hot path this fires twice per call (websearch group + outer AND).
func parenJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return "()"
	}
	n := 2 + len(sep)*(len(parts)-1)
	for _, p := range parts {
		n += len(p)
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteByte('(')
	for i, p := range parts {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p)
	}
	b.WriteByte(')')
	return b.String()
}

// ---------------------------------------------------------------------------
// runVectorSearch
// ---------------------------------------------------------------------------

// buildSetEfSearchSQL builds the per-transaction `SET LOCAL hnsw.ef_search`
// statement. Extracted as a pure helper so the value plumbing is unit-testable
// without spinning up a database.
//
// Note on fmt.Sprintf for security audits: PostgreSQL's SET command does NOT
// accept bound parameters (`SET LOCAL hnsw.ef_search = $1` is a syntax error),
// so direct formatting is the only option. The value is typed as `int` —
// caller-controlled values cannot reach this builder.
func buildSetEfSearchSQL(efSearch int) string {
	return fmt.Sprintf("SET LOCAL hnsw.ef_search = %d", efSearch)
}

// buildTwoPassVectorSQL builds the MRL two-pass vector search.
//
// Bound parameters (caller is responsible for ordering args this way):
//
//	$1 = low-dim query vector (vector(256), L2-normalized first 256 components)
//	$2 = kb_id (uuid)
//	$3 = candidate over-fetch limit (int) — the inner LIMIT
//	$4 = full-dim query vector (vector or vector-cast-to-halfvec literal)
//	$5 = final limit (int) — the outer LIMIT
//	$6 (optional) = file_ids array (uuid[]) — referenced via filterClause when present
//
// SQL INJECTION INVARIANT: tableName is produced exclusively by
// GetVectorTableName(dimensions), which returns only "document_chunks" or
// "document_chunks_{int}". dimensions is an int from config. Any
// caller-controlled values (query embeddings, kbID, fileIDs) are passed
// positionally via args and bound by pgx.
func buildTwoPassVectorSQL(tableName, filterClause string, dimensions int, useHalfvec bool) string {
	var fullVecCast, fullEmbExpr string
	if useHalfvec {
		fullVecCast = fmt.Sprintf("$4::vector::halfvec(%d)", dimensions)
		fullEmbExpr = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	} else {
		fullVecCast = "$4::vector"
		fullEmbExpr = "embedding"
	}

	return fmt.Sprintf(`
		WITH candidates AS (
			SELECT id, content, COALESCE(contextual_prefix, '') AS prefix,
			       metadata::text AS metadata, file_id::text AS file_id,
			       embedding,
			       COALESCE(parent_chunk_id::text, '') AS parent_chunk_id,
			       COALESCE(node_kind, 'leaf') AS node_kind,
			       COALESCE(tree_level, 0)    AS tree_level
			FROM "%s"
			WHERE %s
			ORDER BY embedding_low <=> $1::vector(256)
			LIMIT $3
		)
		SELECT id::text, content, prefix, metadata, file_id,
		       1 - (%s <=> %s) AS score,
		       parent_chunk_id, node_kind, tree_level
		FROM candidates
		ORDER BY %s <=> %s
		LIMIT $5
	`, tableName, filterClause, fullEmbExpr, fullVecCast, fullEmbExpr, fullVecCast)
}

// rawRow is an intermediate result from a DB query before RankedDoc conversion.
type rawRow struct {
	ID               string
	Content          string
	ContextualPrefix string          // LLM-generated 1-sentence context; empty when enrichment was off at ingestion time
	Metadata         json.RawMessage // raw JSON; pgx scans straight into the byte slice so toRankedDocs hands it through without a string→[]byte copy on the search hot path
	FileID           string
	Score            float64 // vector similarity or keyword rank
	// ParentChunkID is the FK to document_chunk_parents.id when the row is
	// a Phase 3 §D parent-child child chunk. Empty string for legacy flat
	// chunks (the SQL casts NULL → empty via COALESCE in the SELECT lists).
	ParentChunkID string
	// NodeKind is "leaf" for ordinary chunks and "summary" for Phase F
	// RAPTOR-built summary nodes. Empty/pre-0046 reads are normalised
	// to "leaf" by toRankedDocs.
	NodeKind string
	// TreeLevel is 0 for leaves, 1..N for RAPTOR summaries. Surfaced in
	// the answer prompt via renderSourceHeader.
	TreeLevel int
}

func (s *SearchService) runVectorSearch(
	ctx context.Context,
	tableName, embeddingJSON, kbID string,
	fileIDs []string,
	limit, dimensions int,
	useHalfvec bool,
	efSearch int,
	mrlTwoPass bool,
	embeddingLowJSON, nodeKindFilter string,
) ([]rawRow, error) {
	// Use two-pass when enabled AND we have a low-dim query vector. If
	// the low-dim vector is empty (caller didn't compute one, or embedding
	// was too short to truncate), silently fall back to single-pass.
	useTwoPass := mrlTwoPass && embeddingLowJSON != ""

	mode := "single_pass"
	if useTwoPass {
		mode = "two_pass"
		observability.RecordMRLTwoPassUsed()
	}
	startSearch := time.Now()
	defer func() {
		observability.RecordVectorSearch(mode, time.Since(startSearch).Seconds())
	}()

	var vectorSQL string
	var args []any

	if useTwoPass {
		// Two-pass arg order:
		//   $1=low-dim query, $2=kb_id, $3=over-fetch limit,
		//   $4=full-dim query, $5=final limit, $6 (optional)=file_ids,
		//   $N (optional)=node_kind text (Phase F eval ablation)
		filterClause := "kb_id = $2::uuid"
		args = []any{embeddingLowJSON, kbID, limit * 5, embeddingJSON, limit}
		nextParam := 6
		if len(fileIDs) > 0 {
			filterClause += " AND file_id = ANY($6::uuid[])"
			args = append(args, fileIDs)
			nextParam = 7
		}
		if nodeKindFilter != "" {
			filterClause += fmt.Sprintf(" AND node_kind = $%d::text", nextParam)
			args = append(args, nodeKindFilter)
		}
		filterClause += excludeCommunitySummaryClause(nodeKindFilter)
		vectorSQL = buildTwoPassVectorSQL(tableName, filterClause, dimensions, useHalfvec)
	} else {
		// Single-pass (existing behavior).
		filterClause := "kb_id = $2::uuid"
		args = []any{embeddingJSON, kbID}
		nextParam := 3
		if len(fileIDs) > 0 {
			filterClause += " AND file_id = ANY($3::uuid[])"
			args = append(args, fileIDs)
			nextParam = 4
		}
		if nodeKindFilter != "" {
			filterClause += fmt.Sprintf(" AND node_kind = $%d::text", nextParam)
			args = append(args, nodeKindFilter)
		}
		filterClause += excludeCommunitySummaryClause(nodeKindFilter)

		var vectorCast, embeddingExpr string
		if useHalfvec {
			vectorCast = fmt.Sprintf("$1::vector::halfvec(%d)", dimensions)
			embeddingExpr = fmt.Sprintf("embedding::halfvec(%d)", dimensions)
		} else {
			vectorCast = "$1::vector"
			embeddingExpr = "embedding"
		}

		// SQL INJECTION INVARIANT: see buildTwoPassVectorSQL doc — same
		// reasoning applies here. tableName is GetVectorTableName output,
		// dimensions is an int, the $N positions are literals, LIMIT %d is
		// bounded.
		vectorSQL = fmt.Sprintf(`
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       1 - (%s <=> %s) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "%s"
			WHERE %s
			ORDER BY %s <=> %s
			LIMIT %d
		`, embeddingExpr, vectorCast, tableName, filterClause,
			embeddingExpr, vectorCast, limit)
	}

	// When efSearch is unset (<= 0), skip the BEGIN/SET LOCAL/COMMIT cycle
	// entirely — there's no parameter to scope. This shaves three round-trips
	// per search and avoids the pathological `SET LOCAL hnsw.ef_search = 0`
	// that pgvector would either reject or silently saturate. Production
	// paths always go through DefaultConfig() (HNSWEfSearch=150) so this
	// branch is defence-in-depth against any KBVectorConfig{} zero-value
	// caller.
	if efSearch <= 0 {
		rows, err := s.vectorDB.Query(ctx, vectorSQL, args...)
		if err != nil {
			return nil, fmt.Errorf("vector search: query: %w", err)
		}
		results, err := collectRawRows(rows, limit)
		if err != nil {
			return nil, fmt.Errorf("vector search: collect rows: %w", err)
		}
		return results, nil
	}

	// Pipeline BEGIN/SET LOCAL/SELECT/COMMIT in a single pgx.Batch so the
	// four statements go out as one network round-trip. pgxpool.SendBatch
	// acquires one connection, writes all queued statements through the
	// extended protocol in a single flush, then reads responses
	// incrementally. The transaction is still scoped to this batch because
	// the BEGIN/COMMIT pair runs on the same acquired connection. The
	// prior pgxutil.WithTxOptions path issued BEGIN, SET LOCAL, SELECT,
	// and COMMIT as four separate round-trips.
	batch := &pgx.Batch{}
	batch.Queue("BEGIN")
	batch.Queue(buildSetEfSearchSQL(efSearch))
	batch.Queue(vectorSQL, args...)
	batch.Queue("COMMIT")

	br := s.vectorDB.SendBatch(ctx, batch)
	// Close drains any unread results and releases the connection. Safe to
	// call after explicit Exec/Query reads below — it's a no-op when fully
	// drained. Must come last in the cleanup chain.
	defer br.Close()

	if _, err := br.Exec(); err != nil {
		return nil, fmt.Errorf("vector search: begin: %w", err)
	}
	if _, err := br.Exec(); err != nil {
		return nil, fmt.Errorf("vector search: set ef_search: %w", err)
	}
	rows, err := br.Query()
	if err != nil {
		return nil, fmt.Errorf("vector search: query: %w", err)
	}
	results, err := collectRawRows(rows, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: collect rows: %w", err)
	}
	if _, err := br.Exec(); err != nil {
		return nil, fmt.Errorf("vector search: commit: %w", err)
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// runKeywordSearch
// ---------------------------------------------------------------------------

func (s *SearchService) runKeywordSearch(
	ctx context.Context,
	tableName, query, kbID, pgConfig string,
	fileIDs []string,
	limit int,
	arm keywordArmSettings,
	nodeKindFilter string,
) ([]rawRow, error) {
	// arm.Mode is resolved exactly once per Search() call (including the
	// bm25-stats-availability fallback and its metrics) and reused
	// unchanged by every keyword-arm fan-out — see the keywordArmSettings
	// doc comment. Callers outside Search() (the KeywordSearch MCP tool)
	// resolve their own mode the same way before calling in.
	mode := arm.Mode
	if mode == "" {
		mode = KeywordScoringTsRank
	}

	keywordSQL, args, ok := buildKeywordSQL(keywordSQLInput{
		TableName:      tableName,
		Query:          query,
		KbID:           kbID,
		PgConfig:       pgConfig,
		FileIDs:        fileIDs,
		Limit:          limit,
		SimpleArm:      arm.SimpleArm,
		NodeKindFilter: nodeKindFilter,
		Mode:           mode,
		Dim:            arm.Dim,
		K1:             arm.K1,
		B:              arm.B,
	})
	if !ok {
		return nil, nil
	}

	rows, err := s.vectorDB.Query(ctx, keywordSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: query: %w", err)
	}

	results, err := collectRawRows(rows, limit)
	if err != nil {
		return nil, fmt.Errorf("keyword search: collect rows: %w", err)
	}

	return results, nil
}
