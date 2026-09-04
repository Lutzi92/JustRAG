// Package rematerialize exposes the per-KB tabular rematerialize endpoint:
// how an operator applies changed tabular_* settings to already-ingested
// spreadsheet files without re-uploading them.
package rematerialize

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
)

// Enqueuer is the asynq client surface this handler needs.
type Enqueuer interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// FileLister is the file-store surface this handler needs.
type FileLister interface {
	ListSpreadsheetFiles(ctx context.Context, kbID string) ([]*files.FileInfo, error)
}

// SiteConfigReader gates the endpoint on chat_tabular_query_enabled.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Handler enqueues a full re-ingest of every spreadsheet file in a KB.
//
// Ruling: rematerialise IS the standard re-embedding pipeline (delete a
// file's chunks, re-run ProcessFile) — there is no tabular-only job type.
// ProcessFile now also rebuilds a spreadsheet's SQL tables via the tabular
// ingester (which drops the old ones first), so routing tabular_* setting
// changes through TypeReEmbedding refreshes both the chunks and the tables
// in one pass. Non-spreadsheet files in the KB are untouched.
type Handler struct {
	enq   Enqueuer
	files FileLister
	cfg   SiteConfigReader
}

// NewHandler builds the handler.
func NewHandler(enq Enqueuer, fileLister FileLister, cfg SiteConfigReader) *Handler {
	return &Handler{enq: enq, files: fileLister, cfg: cfg}
}

func kbIDFrom(ctx context.Context, r *http.Request) string {
	if access := kbaccess.AccessFromContext(ctx); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// PostRematerialize handles POST /api/kb/{id}/tabular/rematerialize.
//
// 202 {"status":"queued","kbId":…,"files":N} on success, where N is the
// number of files actually enqueued (a file with no storage path is skipped
// with a warning and not counted). 409 {"error":"chat_tabular_query_enabled
// is off"} when the master flag is off.
func (h *Handler) PostRematerialize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFrom(ctx, r)

	if !chat.ChatTabularQueryEnabled(ctx, h.cfg) {
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "chat_tabular_query_enabled is off")
		return
	}

	fileRows, err := h.files.ListSpreadsheetFiles(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("tabular rematerialize: list files failed", "kb_id", kbID, "err", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list spreadsheet files")
		return
	}

	queued := 0
	for _, f := range fileRows {
		if f.StoragePath == nil || *f.StoragePath == "" {
			logctx.From(ctx).Warn("tabular rematerialize: skipping file with no storage path", "kb_id", kbID, "file_id", f.ID)
			continue
		}
		payload, err := json.Marshal(jobs.FileProcessingPayload{
			FileID:       f.ID,
			KbID:         f.KbID,
			FilePath:     *f.StoragePath,
			OriginalName: f.Name,
			MimeType:     f.Type,
		})
		if err != nil {
			logctx.From(ctx).Warn("tabular rematerialize: marshal payload failed", "kb_id", kbID, "file_id", f.ID, "err", err)
			continue
		}
		if _, err := h.enq.EnqueueContext(ctx,
			asynq.NewTask(jobs.TypeReEmbedding, payload),
			asynq.Queue(jobs.QueueBatch),
			asynq.MaxRetry(1),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeReEmbedding)),
		); err != nil {
			logctx.From(ctx).Warn("tabular rematerialize: enqueue failed", "kb_id", kbID, "file_id", f.ID, "err", err)
			continue
		}
		queued++
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusAccepted, map[string]any{
		"status": "queued",
		"kbId":   kbID,
		"files":  queued,
	})
}
