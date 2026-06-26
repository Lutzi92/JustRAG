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

// CommunitiesHandler enqueues the KG community-detection batch job for a KB.
// It reuses the Enqueuer and CanonReader interfaces declared by the
// canonicalize handler in this package.
type CommunitiesHandler struct {
	enq     Enqueuer
	reader  CanonReader
	enabled func(ctx context.Context, r CanonReader) bool
}

// NewCommunitiesHandler builds the handler. enabled gates the endpoint (503 when off).
func NewCommunitiesHandler(enq Enqueuer, reader CanonReader, enabled func(ctx context.Context, r CanonReader) bool) *CommunitiesHandler {
	return &CommunitiesHandler{enq: enq, reader: reader, enabled: enabled}
}

// PostBuildCommunities handles POST /api/kb/{id}/communities/build.
func (h *CommunitiesHandler) PostBuildCommunities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFrom(ctx, r)
	if !h.enabled(ctx, h.reader) {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "community build is disabled")
		return
	}
	payload, err := json.Marshal(jobs.KGCommunitiesBuildPayload{KbID: kbID})
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to build community build payload")
		return
	}
	if _, err := h.enq.EnqueueContext(ctx,
		asynq.NewTask(jobs.TypeKGCommunitiesBuild, payload),
		asynq.Queue(jobs.QueueBatch),
		asynq.Timeout(jobs.TimeoutFor(jobs.TypeKGCommunitiesBuild)),
		asynq.MaxRetry(1),
	); err != nil {
		logctx.From(ctx).Warn("communities: enqueue failed", "kb_id", kbID, "err", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to enqueue community build")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusAccepted, map[string]any{"status": "queued", "kbId": kbID})
}
