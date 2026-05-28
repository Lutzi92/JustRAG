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
	simpleArm, tieredBoost bool,
	nodeKindFilter string,
) ([]rawRow, error) {
	phrases, remainder := extractQuotedPhrases(query)
	// Email literals: pulled out of remainder and promoted to phrases so
	// phraseto_tsquery exact-matches them (the default text-search parser keeps
	// emails as one token; bare-token tokenisation in keyword_query.go would
	// shred them on `@` and `.`).
	if emails, rest := extractEmailLiterals(remainder); len(emails) > 0 {
		phrases = append(phrases, emails...)
		remainder = rest
	}
	if remainder == "" && len(phrases) == 0 {
		return nil, nil
	}

	// Build parameters: kb_id is $1, pgConfig is $2. When the simple-arm
	// is enabled, the literal `simple` regconfig occupies $3 so the two
	// arms (language stemmer vs. surface form) share the SAME query-text
	// placeholders — args binding stays single-bind and the composed
	// expression is generated for arm-1 then string-substituted for arm-2.
	args := []any{kbID, pgConfig}
	simpleConfigParam := 0 // 0 = arm disabled; else the placeholder index of 'simple'
	nextParam := 3
	if simpleArm {
		args = append(args, "simple")
		simpleConfigParam = nextParam
		nextParam++
	}

	filterClause := "kb_id = $1::uuid"
	if len(fileIDs) > 0 {
		filterClause += fmt.Sprintf(" AND file_id = ANY($%d::uuid[])", nextParam)
		args = append(args, fileIDs)
		nextParam++
	}
	if nodeKindFilter != "" {
		// Phase F eval ablation: restrict the keyword arm to leaves
		// or summaries. Must be applied before the tsvector match
		// composition below since composed query parts use sequential
		// $N indices and we need the placeholder to land here.
		filterClause += fmt.Sprintf(" AND node_kind = $%d::text", nextParam)
		args = append(args, nodeKindFilter)
		nextParam++
	}

	// websearchClause is hoisted to function scope so the tiered-boost CASE
	// (applied to the SELECT's score expression below when tieredBoost is on)
	// can reference it. Empty when remainder is empty — no strict-form text
	// query to differentiate, so the boost is skipped for phrases-only queries.
	var websearchClause string
	var queryParts []string
	if remainder != "" {
		args = append(args, remainder)
		websearchClause = fmt.Sprintf("websearch_to_tsquery($2::regconfig, $%d)", nextParam)
		nextParam++

		// Detected proper-noun phrases ("Eberhard Kurz", "Marcus Enger") get
		// OR-folded into the websearch group as phraseto_tsquery clauses. The
		// production websearch_to_tsquery ANDs every content stem in the query,
		// so entity-role lookups like "Welche Rolle übernimmt Eberhard Kurz"
		// require chunks that contain both the entity AND the question verb —
		// a co-occurrence that fails for typical role-statement chunks
		// ("Projektleitung: Eberhard Kurz") whose verbs differ from the
		// question's. Folding the name as an OR'd phrase recovers those chunks
		// for the reranker to score.
		//
		// Gated to entity-asking queries (who/role/function patterns) because
		// the bare extractor over-extracts on German — every adjacent pair of
		// capitalized common nouns ("Künstliche Intelligenz", "Best Practices")
		// would otherwise broaden the BM25 pool and dilute reranker
		// discrimination on complex_reasoning queries (eval q085fix
		// 2026-05-07: ungated cost complex_reasoning MRR −3.2pp).
		var detectedClauses []string
		if isEntityAskingQuery(query) {
			for _, p := range extractProperNounPhrases(remainder) {
				args = append(args, p)
				detectedClauses = append(detectedClauses, fmt.Sprintf("phraseto_tsquery($2::regconfig, $%d)", nextParam))
				nextParam++
			}
		}

		orTokens, hasOrTokens := buildOrTokensExpr(remainder)
		var orClause string
		if hasOrTokens {
			args = append(args, orTokens)
			orClause = fmt.Sprintf("to_tsquery($2::regconfig, $%d)", nextParam)
			nextParam++
		}

		// Compose the websearch group: websearch (AND-required), optionally
		// OR'd with the OR-of-tokens recall floor, optionally OR'd with each
		// detected proper-noun phrase. The reranker breaks ties downstream.
		groupAlts := []string{websearchClause}
		if hasOrTokens {
			groupAlts = append(groupAlts, orClause)
		}
		groupAlts = append(groupAlts, detectedClauses...)
		if len(groupAlts) == 1 {
			queryParts = append(queryParts, websearchClause)
		} else {
			queryParts = append(queryParts, parenJoin(groupAlts, " || "))
		}
	}
	for _, p := range phrases {
		args = append(args, p)
		queryParts = append(queryParts, fmt.Sprintf("phraseto_tsquery($2::regconfig, $%d)", nextParam))
		nextParam++
	}

	// If both the remainder and the phrase list produced zero clauses, there is
	// nothing to search for. (The early return for query=="" higher up already
	// covers most of this; this branch handles the corner case where remainder
	// is non-empty but contains no phrases AND no characters at all in queryParts
	// for some reason — defensive.)
	if len(queryParts) == 0 {
		return nil, nil
	}

	// Compose with && (AND) at the top level: each phrase clause is required,
	// and the unquoted-remainder sub-clause (which itself is an OR-union of the
	// AND-required and OR-of-tokens variants) is also required to match
	// something. Wrap in parens so PG parses correctly.
	composed := parenJoin(queryParts, " && ")

	// SQL INJECTION INVARIANT:
	//   - tableName: produced exclusively by GetVectorTableName(dimensions),
	//     which returns only "document_chunks" or "document_chunks_{int}".
	//     Both match validVectorTable.MatchString — never user-supplied data.
	//   - composed: a string of the form
	//     "((websearch_to_tsquery($2::regconfig, $N) || to_tsquery($2::regconfig, $M)
	//        || phraseto_tsquery($2::regconfig, $P) …)
	//      && phraseto_tsquery($2::regconfig, $K) …)"
	//     — only $-placeholder numbers and the three whitelisted PG function
	//     names; no user data embedded. Detected proper-noun phrases are
	//     OR'd into the websearch group; quoted phrases / emails remain
	//     AND-required at the top level.
	//   - nextParam: monotonically-incremented int placeholder counter ($1, $2, …).
	//   - LIMIT %d: int value bounded by caller.
	// All user-controllable values (kbID, pgConfig, fileIDs, query text, phrases)
	// are passed positionally via args and bound by pgx. The remainder text is
	// passed twice — once to websearch_to_tsquery and once (as the OR-tokens
	// string from buildOrTokensExpr, which strips to_tsquery operator characters
	// at tokenization time) to to_tsquery.
	// When the simple arm is enabled, generate a parallel composed
	// expression that points every regconfig at the simple-config
	// placeholder. String-substituting `$2::regconfig` → `$N::regconfig`
	// is safe — composed contains only $-placeholder digits and the
	// whitelisted PG function names (see invariant comment above), no
	// user data. The two arms OR-fuse in the WHERE clause; ts_rank
	// values are summed so a chunk matching either arm contributes,
	// and a chunk matching both contributes more.
	//
	// Tiered-boost CASE: when tieredBoost is on AND remainder produced
	// a strict-form websearchClause, multiply the language-stemmer
	// ts_rank by 100 (chunk satisfies the AND-required form) or 10
	// (chunk only satisfies the OR-tokens recall floor / proper-noun
	// fallback). Single-token strict queries collapse to a uniform
	// ×100 — order-preserving, so RRF is unaffected. Phrases-only
	// queries (websearchClause unset) get ×1, a no-op. The boost is
	// applied to the main (language-stemmed) arm only; the simple-arm
	// contribution stays unboosted because a simple-arm-only match
	// (e.g. an unstemmed surname) does not imply the strict tsvector
	// matched, so it has no claim to the ×100 tier.
	boostExpr := "1"
	if tieredBoost && websearchClause != "" {
		boostExpr = fmt.Sprintf("CASE WHEN vector_index @@ %s THEN 100 ELSE 10 END", websearchClause)
	}
	var keywordSQL string
	if simpleConfigParam > 0 {
		composedSimple := strings.ReplaceAll(composed, "$2::regconfig", fmt.Sprintf("$%d::regconfig", simpleConfigParam))
		keywordSQL = fmt.Sprintf(`
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       (ts_rank(vector_index, %s) * %s + COALESCE(ts_rank(vector_index_simple, %s), 0)) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "%s"
			WHERE %s AND (vector_index @@ %s OR vector_index_simple @@ %s)
			ORDER BY score DESC
			LIMIT %d
		`, composed, boostExpr, composedSimple, tableName, filterClause, composed, composedSimple, limit)
	} else {
		keywordSQL = fmt.Sprintf(`
			SELECT id::text, content, COALESCE(contextual_prefix, ''), metadata::text, file_id::text,
			       (ts_rank(vector_index, %s) * %s) AS score,
			       COALESCE(parent_chunk_id::text, ''),
			       COALESCE(node_kind, 'leaf'),
			       COALESCE(tree_level, 0)
			FROM "%s"
			WHERE %s AND vector_index @@ %s
			ORDER BY score DESC
			LIMIT %d
		`, composed, boostExpr, tableName, filterClause, composed, limit)
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
