package kggraph

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
)

// Enqueuer is the asynq client surface this handler needs.
type Enqueuer interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// CanonReader gates the endpoint.
type CanonReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// CanonicalizeHandler enqueues the entity-canonicalization batch job for a KB.
type CanonicalizeHandler struct {
	enq     Enqueuer
	reader  CanonReader
	enabled func(ctx context.Context, r CanonReader) bool
}

// NewCanonicalizeHandler builds the handler. enabled gates the endpoint (503 when off).
func NewCanonicalizeHandler(enq Enqueuer, reader CanonReader, enabled func(ctx context.Context, r CanonReader) bool) *CanonicalizeHandler {
	return &CanonicalizeHandler{enq: enq, reader: reader, enabled: enabled}
}

// PostCanonicalize handles POST /api/kb/{id}/canonicalize.
func (h *CanonicalizeHandler) PostCanonicalize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFrom(ctx, r)
	if !h.enabled(ctx, h.reader) {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "entity canonicalization is disabled")
		return
	}
	payload, err := json.Marshal(jobs.EntityCanonicalizePayload{KbID: kbID})
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to build canonicalization payload")
		return
	}
	if _, err := h.enq.EnqueueContext(ctx,
		asynq.NewTask(jobs.TypeEntityCanonicalizeKB, payload),
		asynq.Queue(jobs.QueueBatch),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeEntityCanonicalizeKB)),
		asynq.MaxRetry(1),
	); err != nil {
		logctx.From(ctx).Warn("canonicalize: enqueue failed", "kb_id", kbID, "err", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to enqueue canonicalization")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusAccepted, map[string]any{"status": "queued", "kbId": kbID})
}
