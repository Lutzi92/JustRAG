// Package adminmaintenance hosts admin-only maintenance HTTP handlers that
// don't belong to a single feature package: bulk re-embedding sweeps and
// agent-template uploads. Keeping them here lets routes.go stay pure wiring
// and allows the handlers to be unit-tested in isolation.
package adminmaintenance

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// maxTemplateFilenameLen caps the stored filename. POSIX NAME_MAX is 255 but
// 128 is more than enough for any reasonable template name and avoids odd
// filesystem corner cases.
const maxTemplateFilenameLen = 128

// sanitizeTemplateFilename returns a safe basename derived from raw or
// ("", false) if the result is empty or fails the allowlist check.
// filepath.Base strips any directory components; the allowlist further
// rejects whitespace, control characters, and shell metacharacters that
// could complicate downstream tooling.
func sanitizeTemplateFilename(raw string) (string, bool) {
	base := filepath.Base(raw)
	if base == "" || base == "." || base == ".." || base == "/" {
		return "", false
	}
	if len(base) > maxTemplateFilenameLen {
		return "", false
	}
	for _, r := range base {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '.':
		default:
			return "", false
		}
	}
	// Refuse names that are pure dots ("...") or that start with a dot —
	// neither is useful for templates and the latter is awkward on Unix.
	if strings.Trim(base, ".") == "" || strings.HasPrefix(base, ".") {
		return "", false
	}
	return base, true
}

// KBStore is the subset of kb.PGStore the reembed-all handler depends on.
type KBStore interface {
	ListAllKBIDs(ctx context.Context) ([]string, error)
	ListReembedableFilesByKBID(ctx context.Context, kbID string) ([]kb.FileReembedRow, error)
}

// Enqueuer is the subset of *asynq.Client the reembed-all handler depends on.
type Enqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// MemoryListStore lists the users that have memory rows to re-embed.
type MemoryListStore interface {
	ListUserIDsWithMemory(ctx context.Context) ([]string, error)
}

// Handler bundles maintenance HTTP endpoints behind a single struct so each
// can be wired independently into the mux.
type Handler struct {
	store    KBStore
	enqueuer Enqueuer
	memStore MemoryListStore

	// templateDir is the on-disk directory where uploaded agent template
	// files are written. Default "uploads/templates"; override only in tests.
	templateDir string

	// maxTemplateUploadBytes caps the multipart parse buffer for the upload
	// endpoint. Default 10 MiB.
	maxTemplateUploadBytes int64
}

// NewHandler constructs a Handler with default upload directory and size caps.
func NewHandler(store KBStore, enqueuer Enqueuer, memStore MemoryListStore) *Handler {
	return &Handler{
		store:                  store,
		enqueuer:               enqueuer,
		memStore:               memStore,
		templateDir:            "uploads/templates",
		maxTemplateUploadBytes: 10 << 20,
	}
}

// ReembedAll iterates every KB, lists its completed files, and enqueues a
// re-embedding task per file onto the heavy queue. Failures on individual
// files are logged but do not abort the sweep — the response carries the
// total count of successfully queued jobs.
func (h *Handler) ReembedAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	kbIDs, err := h.store.ListAllKBIDs(ctx)
	if err != nil {
		slog.Error("reembed-all: list KB IDs failed", "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to list knowledge bases")
		return
	}

	queued := 0
	for _, kbID := range kbIDs {
		fileRows, err := h.store.ListReembedableFilesByKBID(ctx, kbID)
		if err != nil {
			slog.Warn("reembed-all: list files failed", "kbId", kbID, "error", err)
			continue
		}
		for _, f := range fileRows {
			if f.StoragePath == nil || *f.StoragePath == "" {
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
				slog.Warn("reembed-all: marshal payload failed", "fileId", f.ID, "error", err)
				continue
			}
			if _, err := h.enqueuer.Enqueue(
				asynq.NewTask(jobs.TypeReEmbedding, payload),
				asynq.Queue(jobs.QueueHeavy),
				asynq.MaxRetry(3),
				asynq.Timeout(jobs.TimeoutFor(jobs.TypeReEmbedding)),
			); err != nil {
				slog.Warn("reembed-all: enqueue failed", "fileId", f.ID, "error", err)
				continue
			}
			queued++
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"success": true, "queued": queued})
}

// ReembedKB handles POST /api/kb/{id}/reembed. KB admin permission is enforced by
// kbAdminChain. It lists the KB's re-embeddable files and enqueues a
// re-embedding task per file onto the heavy queue (a KB-wide sweep, same
// semantics as reembed-all). The re-embed worker deletes old chunks and
// reprocesses with the KB's current — now per-KB-aware — config. Files without
// a storage path are skipped. Response: {"queued": N}.
func (h *Handler) ReembedKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(ctx); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	fileRows, err := h.store.ListReembedableFilesByKBID(ctx, kbID)
	if err != nil {
		slog.Error("reembed-kb: list files failed", "kbId", kbID, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list files")
		return
	}

	queued := 0
	for _, f := range fileRows {
		if f.StoragePath == nil || *f.StoragePath == "" {
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
			slog.Warn("reembed-kb: marshal payload failed", "fileId", f.ID, "error", err)
			continue
		}
		if _, err := h.enqueuer.Enqueue(
			asynq.NewTask(jobs.TypeReEmbedding, payload),
			asynq.Queue(jobs.QueueHeavy),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeReEmbedding)),
		); err != nil {
			slog.Warn("reembed-kb: enqueue failed", "fileId", f.ID, "error", err)
			continue
		}
		queued++
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"queued": queued})
}

// ReembedUserMemory enqueues one re-embed-user-memory job per user that
// has memory rows. Admin-triggered after an embedder change re-dimensioned
// the user_memory.embedding column (existing rows hold zero vectors until
// this runs).
func (h *Handler) ReembedUserMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userIDs, err := h.memStore.ListUserIDsWithMemory(ctx)
	if err != nil {
		slog.Error("reembed-user-memory: list users failed", "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list users with memory")
		return
	}
	queued := 0
	for _, uid := range userIDs {
		payload, err := json.Marshal(jobs.ReEmbedUserMemoryPayload{UserID: uid})
		if err != nil {
			slog.Warn("reembed-user-memory: marshal failed", "userId", uid, "error", err)
			continue
		}
		if _, err := h.enqueuer.Enqueue(
			asynq.NewTask(jobs.TypeReEmbedUserMemory, payload),
			asynq.Queue(jobs.QueueHeavy),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeReEmbedUserMemory)),
		); err != nil {
			slog.Warn("reembed-user-memory: enqueue failed", "userId", uid, "error", err)
			continue
		}
		queued++
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"success": true, "queued": queued})
}

// UploadAgentTemplate accepts a multipart "template" file and stores it
// under templateDir using a sanitised basename. filepath.Base alone strips
// directory traversal sequences; sanitizeTemplateFilename additionally
// enforces an allowlist of safe characters and a length cap.
func (h *Handler) UploadAgentTemplate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(h.maxTemplateUploadBytes); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "failed to parse multipart form")
		return
	}

	file, header, err := r.FormFile("template")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "template file is required")
		return
	}
	defer file.Close()

	safeName, ok := sanitizeTemplateFilename(header.Filename)
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "filename must be alphanumeric with -, _, or . (max 128 chars) and not start with a dot")
		return
	}

	if mkErr := os.MkdirAll(h.templateDir, 0755); mkErr != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create upload directory")
		return
	}

	dst, createErr := os.Create(filepath.Join(h.templateDir, safeName))
	if createErr != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to save file")
		return
	}
	defer dst.Close()

	if _, copyErr := io.Copy(dst, file); copyErr != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to write file")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"success": true, "filename": safeName})
}
