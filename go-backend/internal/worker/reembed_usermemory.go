package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/longmem"
)

// reEmbedMemStore is the minimal longmem surface this handler needs.
type reEmbedMemStore interface {
	ListByUser(ctx context.Context, userID string) ([]longmem.Memory, error)
	UpdateEmbedding(ctx context.Context, userID string, id int64, embedding []float64) error
}

// embedFunc embeds a batch of texts with the active embedder. Injected so
// the handler is unit-testable without an LLM round-trip.
type embedFunc func(ctx context.Context, texts []string) ([][]float64, error)

// NewReEmbedUserMemoryHandler re-embeds a single user's live memory rows.
// Per-row update failures are logged, never abort the sweep — a stuck row
// is retried on the next admin-triggered run.
func NewReEmbedUserMemoryHandler(store reEmbedMemStore, embed embedFunc) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var p jobs.ReEmbedUserMemoryPayload
		if err := json.Unmarshal(task.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal re-embed-user-memory payload: %w", err)
		}
		if p.UserID == "" {
			return fmt.Errorf("re-embed-user-memory: empty userId")
		}
		rows, err := store.ListByUser(ctx, p.UserID)
		if err != nil {
			return fmt.Errorf("re-embed-user-memory: list rows: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		texts := make([]string, len(rows))
		for i, r := range rows {
			texts[i] = r.Content
		}
		embs, err := embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("re-embed-user-memory: embed: %w", err)
		}
		updated := 0
		for i, r := range rows {
			if i >= len(embs) || len(embs[i]) == 0 {
				continue
			}
			if uerr := store.UpdateEmbedding(ctx, p.UserID, r.ID, embs[i]); uerr != nil {
				logctx.From(ctx).Warn("re-embed-user-memory: row update failed",
					"user_id", p.UserID, "row_id", r.ID, "err", uerr)
				continue
			}
			updated++
		}
		logctx.From(ctx).Info("re-embed-user-memory: done",
			"user_id", p.UserID, "rows", len(rows), "updated", updated)
		return nil
	}
}
