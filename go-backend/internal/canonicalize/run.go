package canonicalize

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/logctx"
)

// EmbedFunc embeds entity names with the KB embedder (in-memory; never stored).
type EmbedFunc func(ctx context.Context, texts []string) ([][]float64, error)

// ConfirmFunc returns the subset of candidate pairs the LLM confirms as the
// same real-world entity. A non-nil error aborts the run.
type ConfirmFunc func(ctx context.Context, ents []CanonEntity, pairs []Pair) ([]Pair, error)

// Merger applies a confirmed merge group. Satisfied by *kg.PgStore.
type Merger interface {
	MergeEntities(ctx context.Context, kbID string, survivorID int64, mergedIDs []int64, aliasUnion []string) error
}

// Config holds the tunables resolved from site config.
type Config struct {
	Threshold float64
	MaxPairs  int
}

// Result summarises a run for logging/metrics.
type Result struct {
	Considered int
	Confirmed  int
	Merged     int
}

// Run is the canonicalization pipeline: embed → candidate pairs → LLM-confirm →
// cluster → per-group merge. Per-group merge errors are logged and skipped
// (best-effort). An embed or confirm error aborts (so Asynq can retry).
func Run(ctx context.Context, kbID string, ents []CanonEntity, embed EmbedFunc, confirm ConfirmFunc, merge Merger, cfg Config) (Result, error) {
	var res Result
	if len(ents) < 2 {
		return res, nil
	}
	names := make([]string, len(ents))
	for i, e := range ents {
		names[i] = e.Name
	}
	embs, err := embed(ctx, names)
	if err != nil {
		return res, fmt.Errorf("canonicalize: embed: %w", err)
	}
	pairs := candidatePairs(ents, embs, cfg.Threshold, cfg.MaxPairs)
	res.Considered = len(pairs)
	if len(pairs) == 0 {
		return res, nil
	}
	confirmed, err := confirm(ctx, ents, pairs)
	if err != nil {
		return res, fmt.Errorf("canonicalize: confirm: %w", err)
	}
	res.Confirmed = len(confirmed)
	if len(confirmed) == 0 {
		return res, nil
	}
	for _, group := range cluster(confirmed, len(ents)) {
		members := make([]CanonEntity, len(group))
		for i, idx := range group {
			members[i] = ents[idx]
		}
		survivorID, mergedIDs, aliasUnion := pickSurvivor(members)
		if len(mergedIDs) == 0 {
			continue
		}
		if err := merge.MergeEntities(ctx, kbID, survivorID, mergedIDs, aliasUnion); err != nil {
			logctx.From(ctx).Warn("canonicalize: merge group failed; continuing",
				"kb_id", kbID, "survivor", survivorID, "merged", len(mergedIDs), "err", err)
			continue
		}
		logctx.From(ctx).Info("canonicalize.merged",
			"kb_id", kbID, "survivor_id", survivorID, "merged_ids", mergedIDs, "aliases", len(aliasUnion))
		res.Merged += len(mergedIDs)
	}
	return res, nil
}
