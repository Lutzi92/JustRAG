package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/canonicalize"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/logctx"
)

// canonStore is the minimal kg surface the handler needs (satisfied by *kg.PgStore).
type canonStore interface {
	EntitiesForCanonicalization(ctx context.Context, kbID string) ([]kg.CanonEntityRow, error)
	MergeEntities(ctx context.Context, kbID string, survivorID int64, mergedIDs []int64, aliasUnion []string) error
}

// CanonicalizeDeps are the injected dependencies (all seams for testability).
// EmbedFor/ConfirmFor are factories closing over the kbID (the embedder + model
// resolve per-KB), built at call time from the payload's KbID.
type CanonicalizeDeps struct {
	Store      canonStore
	Reader     canonicalize.SiteConfigReader
	EmbedFor   func(kbID string) canonicalize.EmbedFunc
	ConfirmFor func(kbID string) canonicalize.ConfirmFunc
	Publish    func(ctx context.Context, kbID string)
}

// NewEntityCanonicalizeHandler runs the admin-triggered KB canonicalization.
// No-op when the gate is off. Best-effort; publishes graph_changed on success.
func NewEntityCanonicalizeHandler(deps CanonicalizeDeps) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var p jobs.EntityCanonicalizePayload
		if err := json.Unmarshal(task.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal entity-canonicalize payload: %w", err)
		}
		if p.KbID == "" {
			return fmt.Errorf("entity-canonicalize: empty kbId")
		}
		if !canonicalize.Enabled(ctx, deps.Reader) {
			logctx.From(ctx).Info("canonicalize: gate off; skipping", "kb_id", p.KbID)
			return nil
		}
		rows, err := deps.Store.EntitiesForCanonicalization(ctx, p.KbID)
		if err != nil {
			return fmt.Errorf("entity-canonicalize: load entities: %w", err)
		}
		ents := make([]canonicalize.CanonEntity, len(rows))
		for i, r := range rows {
			ents[i] = canonicalize.CanonEntity{ID: r.ID, Name: r.Name, Type: r.Type, Aliases: r.Aliases, Degree: r.Degree}
		}
		cfg := canonicalize.Config{
			Threshold: canonicalize.Threshold(ctx, deps.Reader),
			MaxPairs:  canonicalize.MaxPairs(ctx, deps.Reader),
		}
		res, err := canonicalize.Run(ctx, p.KbID, ents, deps.EmbedFor(p.KbID), deps.ConfirmFor(p.KbID), deps.Store, cfg)
		if err != nil {
			return fmt.Errorf("entity-canonicalize: run: %w", err)
		}
		logctx.From(ctx).Info("canonicalize.done",
			"kb_id", p.KbID, "considered", res.Considered, "confirmed", res.Confirmed, "merged", res.Merged)
		if res.Merged > 0 && deps.Publish != nil {
			deps.Publish(ctx, p.KbID)
		}
		return nil
	}
}
