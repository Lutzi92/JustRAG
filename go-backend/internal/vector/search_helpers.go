package vector

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// collectRawRows drains a pgx.Rows into a []rawRow. capacityHint is used as
// the initial slice capacity (clamped to a small floor for the empty case).
//
// Column order — every SELECT that feeds this helper must list columns
// in this exact order:
//
//	id::text, content, COALESCE(contextual_prefix,''), metadata::text,
//	file_id::text, score, COALESCE(parent_chunk_id::text,''),
//	COALESCE(node_kind,'leaf'), COALESCE(tree_level,0)
//
// The last two are Phase F (RAPTOR) — pre-0046 rows have NULL/missing
// values that the COALESCE wrappers normalise to defaults.
func collectRawRows(rows pgx.Rows, capacityHint int) ([]rawRow, error) {
	defer rows.Close()
	if capacityHint < 8 {
		capacityHint = 8
	}
	results := make([]rawRow, 0, capacityHint)
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.ID, &r.Content, &r.ContextualPrefix, &r.Metadata, &r.FileID, &r.Score, &r.ParentChunkID, &r.NodeKind, &r.TreeLevel); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// toRankedDocs converts a slice of rawRows to RankedDocs.
// If isVector is true the score is stored as VectorScore; otherwise as KeywordRank.
func toRankedDocs(rows []rawRow, isVector bool) []RankedDoc {
	docs := make([]RankedDoc, len(rows))
	for i, r := range rows {
		// Defence-in-depth — SQL COALESCEs to 'leaf' already, but a
		// future SELECT that forgets the wrapper would otherwise leak
		// "" into downstream label checks.
		nk := r.NodeKind
		if nk == "" {
			nk = "leaf"
		}
		doc := RankedDoc{
			ID:               r.ID,
			Content:          r.Content,
			ContextualPrefix: r.ContextualPrefix,
			Metadata:         r.Metadata,
			FileID:           r.FileID,
			NodeKind:         nk,
			TreeLevel:        r.TreeLevel,
		}
		if r.ParentChunkID != "" {
			pid := r.ParentChunkID
			doc.ParentChunkID = &pid
		}
		if isVector {
			doc.VectorScore = r.Score
		} else {
			doc.KeywordRank = r.Score
		}
		docs[i] = doc
	}
	return docs
}

// buildMetadataFilterClause appends metadata filter conditions to an existing
// WHERE clause fragment. Each key/value pair becomes a JSONB containment check:
//
//	metadata @> '{"key":"value"}'::jsonb
//
// Returns the updated clause and args slice; paramN is the next available
// positional parameter index.
//
// Currently reserved for future use — not wired into the main pipeline yet.
func buildMetadataFilterClause(
	baseClause string,
	args []any,
	paramN int,
	filters map[string]string,
) (clause string, outArgs []any, nextParam int) {
	clause = baseClause
	outArgs = args
	nextParam = paramN

	for k, v := range filters {
		jsonSnippet, err := json.Marshal(map[string]string{k: v})
		if err != nil {
			continue
		}
		clause += fmt.Sprintf(" AND metadata @> $%d::jsonb", nextParam)
		outArgs = append(outArgs, string(jsonSnippet))
		nextParam++
	}
	return clause, outArgs, nextParam
}

// joinStrings is a local alias used in tests; not part of the public API.
func joinStrings(ss []string) string { return strings.Join(ss, ",") }

// topScore returns the score of the first (highest-ranked) chunk, or 0 if empty.
func topScore(chunks []SearchChunk) float64 {
	if len(chunks) == 0 {
		return 0
	}
	return chunks[0].Score
}

// collectParentIDs returns the unique non-nil ParentChunkID values from
// docs, in the order they first appear. Used by the search pipeline to
// batch-fetch parent content in one query.
func collectParentIDs(docs []RankedDoc) []string {
	seen := make(map[string]struct{}, len(docs))
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		if d.ParentChunkID == nil || *d.ParentChunkID == "" {
			continue
		}
		if _, dup := seen[*d.ParentChunkID]; dup {
			continue
		}
		seen[*d.ParentChunkID] = struct{}{}
		out = append(out, *d.ParentChunkID)
	}
	return out
}

// swapToParents replaces each doc's Content + ContextualPrefix with the
// parent row's content + prefix when the doc has a non-nil ParentChunkID,
// AND collapses multiple children of the same parent into a single slot
// (keep the FIRST occurrence — assumes input is score-sorted, so first =
// highest scored). Without this dedup, MMR's diversity calculation runs
// on child content but post-swap multiple children of the same parent
// become identical text → the LLM context fills with duplicate parents,
// breaking the diversity guarantee MMR was supposed to provide. The
// 2026-05-05 eval regression (overall recall -11.7 pp, MRR -20.3 pp on
// the JLU 89-question set) was traced to this exact failure mode.
//
// Returns (deduped slice, swap count, dropped count). swap count is the
// number of rows whose Content was mutated; dropped count is how many
// duplicate-parent rows were collapsed away. Rows with NULL ParentChunkID
// pass through unchanged. Rows whose parent is missing from the lookup
// map are kept (with their original child content) and still consume
// their dedup slot — fail-open keeps the result set populated when
// parent lookups are flaky.
func swapToParents(docs []RankedDoc, parents map[string]ParentChunkRow) ([]RankedDoc, int, int) {
	swapped := 0
	dropped := 0
	seen := make(map[string]struct{}, len(docs))
	out := make([]RankedDoc, 0, len(docs))
	for i := range docs {
		d := docs[i]
		if d.ParentChunkID == nil || *d.ParentChunkID == "" {
			out = append(out, d)
			continue
		}
		if _, dup := seen[*d.ParentChunkID]; dup {
			dropped++
			continue
		}
		seen[*d.ParentChunkID] = struct{}{}
		if p, ok := parents[*d.ParentChunkID]; ok {
			d.Content = p.Content
			d.ContextualPrefix = p.ContextualPrefix
			swapped++
		}
		out = append(out, d)
	}
	return out, swapped, dropped
}

// routeLabelFor reports the per-route label for telemetry. Empty/unknown
// query types render as "global" so log readers can distinguish "no route
// override applied" from "route override applied for X".
func routeLabelFor(queryType string) string {
	switch queryType {
	case QueryTypeLookup, QueryTypeEnumeration, QueryTypeComplexReasoning:
		return queryType
	default:
		return "global"
	}
}

// stepBackOutcome is the metric label the search pipeline records for a
// given (StepBack flag, QueryType) pair, BEFORE any LLM call. "fired"
// is the only outcome that triggers the LLM; the others bypass it.
// Pure function — no observability dependency, fully unit-testable.
func stepBackOutcome(stepBack bool, queryType string) string {
	if !stepBack {
		return "skipped_disabled"
	}
	if queryType == QueryTypeComplexReasoning {
		return "fired"
	}
	return "skipped_route"
}
