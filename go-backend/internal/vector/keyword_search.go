package vector

import (
	"context"
	"fmt"
	"sync"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// dimFromTableName recovers the embedding dimension from a chunk table
// name produced by GetVectorTableName — "document_chunks" (bare) means the
// legacy 1536-dim default, "document_chunks_<digits>" carries its own dim.
// Used only to resolve the matching bm25_kb_stats_<dim>/bm25_term_stats_<dim>
// tables for the KeywordSearch MCP tool's BM25 scoring mode; falls back to
// 1536 for anything that doesn't parse (defensive — resolveKBChunkTable
// only ever returns validVectorTable-shaped names).
func dimFromTableName(tableName string) int {
	if tableName == "document_chunks" {
		return 1536
	}
	var dim int
	if _, err := fmt.Sscanf(tableName, "document_chunks_%d", &dim); err == nil && dim > 0 {
		return dim
	}
	return 1536
}

// kbTableCache memoises kb_id → chunks-table-name lookups so the
// AP-B1 tools (keyword_search, chunk_read, document_outline) don't
// pay a discovery query per call. The cache is process-wide and
// invalidated on KB re-ingestion via InvalidateKBChunkTable. KBs
// can in principle be moved between dim tables (admin re-ingests
// after switching embedding model), but those migrations are rare
// enough that an explicit invalidate is acceptable.
//
// Keyed on kb_id, value is the table name (e.g. "document_chunks_4096").
type kbTableCache struct {
	m sync.Map
}

// resolveKBChunkTable returns the chunks-table that holds the KB's
// rows. It probes the known dim tables (`ListChunkTableDimensions`)
// and returns the first one that has at least one row for the KB.
//
// Empty KBs (no chunks ingested yet) return an error — the calling
// tool should surface "no chunks" to the LLM instead of guessing a
// table.
func (s *SearchService) resolveKBChunkTable(ctx context.Context, kbID string) (string, error) {
	if v, ok := s.kbTableCache.m.Load(kbID); ok {
		return v.(string), nil
	}
	if s.chunkSvc == nil {
		return "", fmt.Errorf("resolve_kb_chunk_table: chunkSvc not configured")
	}
	dims, err := s.chunkSvc.ListChunkTableDimensions(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve_kb_chunk_table: list dims: %w", err)
	}
	for _, d := range dims {
		table := GetVectorTableName(d)
		// Defence-in-depth: the probe below interpolates `table` into SQL.
		// `table` is produced by GetVectorTableName(int) so it is structurally
		// safe today, but resolveKBChunkTable is the single chokepoint that
		// chunk_read / document_outline / count_mentions all rely on (those
		// callers interpolate the returned name without re-validating). Gate it
		// on the canonical allowlist — the same regex fetchChunksByIDs / hype /
		// search use — so the "whitelisted to document_chunks[_<int>]" contract
		// the callers document is actually enforced here, not merely implied.
		if !validVectorTable.MatchString(table) {
			continue
		}
		var probe int
		err := s.vectorDB.QueryRow(ctx,
			fmt.Sprintf(`SELECT 1 FROM "%s" WHERE kb_id = $1::uuid LIMIT 1`, table),
			kbID,
		).Scan(&probe)
		if err == nil {
			s.kbTableCache.m.Store(kbID, table)
			return table, nil
		}
	}
	return "", fmt.Errorf("resolve_kb_chunk_table: no chunks for kb %s in any dim table", kbID)
}

// InvalidateKBChunkTable drops the cached entry for kbID. Called by
// the worker after re-ingestion that may have changed the active
// embedding dimension. Cheap: a Map.Delete on a missing key is a
// no-op.
func (s *SearchService) InvalidateKBChunkTable(kbID string) {
	s.kbTableCache.m.Delete(kbID)
}

// CountMentions returns the number of chunks in the KB whose content
// matches the given substring (case-insensitive). Used by the
// count_mentions MCP tool — gives the answer-LLM a deterministic answer
// for "how often is X mentioned in this KB" without making it count
// retrieved chunks.
//
// Pattern is treated as a literal substring: the implementation uses
// ILIKE with %-wrapping and escapes %/_/\ in the input so user-supplied
// patterns can never trigger pattern semantics. To match all chunks
// pass pattern="" — which simply returns the total chunk count for the
// KB.
//
// Returns the count and a non-nil error only on DB failure.
func (s *SearchService) CountMentions(ctx context.Context, kbID, pattern string) (int, error) {
	if kbID == "" {
		return 0, fmt.Errorf("count_mentions: kb_id required")
	}
	tableName, err := s.resolveKBChunkTable(ctx, kbID)
	if err != nil {
		return 0, err
	}
	// Escape LIKE metacharacters in pattern so the LLM can't accidentally
	// (or maliciously) inject pattern semantics. Postgres ILIKE shares the
	// LIKE grammar (% and _ wildcards, \ as the default escape char).
	like := "%" + pgxutil.EscapeLike(pattern) + "%"
	// tableName comes from resolveKBChunkTable which is whitelisted to
	// "document_chunks" or "document_chunks_<int>" — safe to interpolate.
	sqlStr := fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE kb_id = $1 AND content ILIKE $2`, tableName)
	var count int
	if err := s.vectorDB.QueryRow(ctx, sqlStr, kbID, like).Scan(&count); err != nil {
		return 0, fmt.Errorf("count_mentions: query: %w", err)
	}
	return count, nil
}

// KeywordSearch runs a BM25-only search over the KB's chunks — no
// embedding, no reranker, no MMR, no BM25 floor. Returns the top-N
// rows in BM25-rank order. Latency target ~80–120ms (vs. ~500–1500ms
// for the full Search() pipeline). Used by the AP-B1 keyword_search
// MCP tool when the agent wants exact-match recall ("does the corpus
// contain string X?") without paying for ranking infrastructure.
//
// fileIDs optional: empty = search the whole KB. limit defaults to
// 10 when caller passes 0 — bounded so the LLM never gets a wall
// of text.
//
// Note: there's still ONE network call hidden in resolveKBChunkTable
// (a probe query). After the first call per KB the cache short-
// circuits subsequent calls to a sync.Map lookup.
func (s *SearchService) KeywordSearch(ctx context.Context, kbID, query string, limit int, fileIDs []string) ([]SearchChunk, error) {
	if kbID == "" {
		return nil, fmt.Errorf("keyword_search: kb_id required")
	}
	if query == "" {
		return nil, fmt.Errorf("keyword_search: query required")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	tableName, err := s.resolveKBChunkTable(ctx, kbID)
	if err != nil {
		return nil, err
	}
	pgConfig := PgTextSearchConfig(s.resolveKBLanguage(ctx, kbID))

	// AP-B1 keyword_search tool path: read the simple-arm + BM25
	// scoring-mode settings from site_config so the agent's tool
	// calls benefit from the same BM25 tuning as the main pipeline when
	// operators enable them. Cached; no extra round-trip on the hot path.
	// Same fail-soft mode resolution as Search(): a KB/dimension without
	// usable stats yet falls back to ts_rank for this call.
	cfg := s.loadSiteConfigCached(ctx)
	dim := dimFromTableName(tableName)
	mode := cfg.BM25ScoringMode
	if mode == KeywordScoringBM25 {
		langAvailable, simpleAvailable := s.bm25ArmAvailability(ctx, kbID, dim)
		var fallbackReason string
		mode, fallbackReason = bm25ModeDecision(mode, cfg.BM25SimpleArmEnabled, langAvailable, simpleAvailable)
		if fallbackReason != "" {
			observability.RecordBM25ModeFallback(fallbackReason)
		}
	}
	observability.RecordKeywordArmMode(string(mode))
	arm := keywordArmSettings{
		SimpleArm: cfg.BM25SimpleArmEnabled,
		Mode:      mode,
		Dim:       dim,
		K1:        cfg.BM25K1,
		B:         cfg.BM25B,
	}
	rows, err := s.runKeywordSearch(ctx, tableName, query, kbID, pgConfig, fileIDs, limit, arm, "")
	if err != nil {
		return nil, fmt.Errorf("keyword_search: %w", err)
	}

	out := make([]SearchChunk, 0, len(rows))
	for _, r := range rows {
		out = append(out, SearchChunk{
			ID:               r.ID,
			Content:          r.Content,
			ContextualPrefix: r.ContextualPrefix,
			FileID:           r.FileID,
			Score:            r.Score,
			NodeKind:         r.NodeKind,
			TreeLevel:        r.TreeLevel,
		})
	}
	return out, nil
}
