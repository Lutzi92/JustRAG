package files

import (
	"context"
	"errors"
	"fmt"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/userfiles"
)

// Statuses of an AddResult.
const (
	AddStatusAdded     = "added"
	AddStatusDuplicate = "duplicate"
	AddStatusError     = "error"
)

// ErrLibraryUnavailable: no user file library is wired into the handler.
var ErrLibraryUnavailable = errors.New("files: user file library not configured")

// ErrInvalidUser: AddLibraryFiles was called without a user id.
var ErrInvalidUser = errors.New("files: user id is required")

// AddResult is the per-id outcome of AddLibraryFiles. For Status "error",
// Error carries the skip reason of the HTTP contract ("not_found" — not in
// the caller's library — or "kb_full").
type AddResult struct {
	UserFileID string `json:"userFileId"`
	FileID     string `json:"fileId,omitempty"`
	Status     string `json:"status"` // "added" | "duplicate" | "error"
	Error      string `json:"error,omitempty"`
}

// libraryAddError is a hard failure of AddLibraryFiles. public is the
// message the HTTP endpoint answers with (status 500).
type libraryAddError struct {
	public string
	err    error
}

func (e *libraryAddError) Error() string {
	return "files: add from library: " + e.public + ": " + e.err.Error()
}
func (e *libraryAddError) Unwrap() error { return e.err }

// AddLibraryFiles is AddFromLibrary's logic without HTTP: owner-scoped
// library lookup, KB file limits, dedup, row creation, enqueue + rollback.
// Per-id problems are reported in the results, never as an error. A
// returned error aborts the remaining ids; the results gathered so far
// (rows already created and enqueued) are returned alongside it.
func (h *Handler) AddLibraryFiles(ctx context.Context, userID, kbID string, isGlobal bool, userFileIDs []string) ([]AddResult, error) {
	if h.library == nil {
		return nil, ErrLibraryUnavailable
	}
	if userID == "" {
		return nil, ErrInvalidUser
	}
	if len(userFileIDs) < 1 || len(userFileIDs) > maxFromLibraryIDs {
		return nil, fmt.Errorf("files: userFileIds must contain 1-%d ids", maxFromLibraryIDs)
	}

	limits, err := h.store.GetKBFileLimits(ctx, kbID)
	if err != nil || limits == nil {
		logctx.From(ctx).Error("from-library: failed to check KB limits", "kbId", kbID, "error", err)
		if err == nil {
			err = errors.New("no KB limits row")
		}
		return nil, &libraryAddError{public: "Failed to check KB limits", err: err}
	}
	// Track the running totals locally so one call cannot overshoot the caps.
	running := *limits

	results := make([]AddResult, 0, len(userFileIDs))
	fail := func(id, reason string) {
		results = append(results, AddResult{UserFileID: id, Status: AddStatusError, Error: reason})
	}
	duplicate := func(id string) {
		results = append(results, AddResult{UserFileID: id, Status: AddStatusDuplicate})
	}

	for _, id := range userFileIDs {
		uf, err := h.library.Get(ctx, userID, id)
		if errors.Is(err, userfiles.ErrNotFound) {
			fail(id, skipNotFound)
			continue
		}
		if err != nil {
			logctx.From(ctx).Error("from-library: library get failed", "error", err)
			return results, &libraryAddError{public: "Internal Server Error", err: err}
		}

		existing, err := h.store.GetKBCopy(ctx, kbID, uf.ID)
		if err != nil {
			logctx.From(ctx).Error("from-library: GetKBCopy failed", "kbId", kbID, "error", err)
			return results, &libraryAddError{public: "Internal Server Error", err: err}
		}
		if existing != "" {
			duplicate(id)
			continue
		}
		if kbFileLimitReached(&running, isGlobal, uf.Size) {
			fail(id, skipKBFull)
			continue
		}

		fileType := uf.Mime
		if len(fileType) > filesTypeMaxBytes {
			fileType = "application/octet-stream"
		}
		rec, err := h.store.CreateFile(ctx, CreateFileData{
			KbID:        kbID,
			UploadedBy:  userID,
			Name:        uf.Name,
			Type:        fileType,
			Size:        int(uf.Size),
			Origin:      "upload",
			StoragePath: uf.StoragePath,
			UserFileID:  uf.ID,
		})
		if err != nil {
			if pgxutil.IsUniqueViolation(err) {
				duplicate(id)
				continue
			}
			logctx.From(ctx).Error("from-library: create file record failed", "kbId", kbID, "error", err)
			return results, &libraryAddError{public: "Failed to create file record", err: err}
		}

		if err := h.enqueueFileProcessing(rec, kbID, uf.StoragePath, uf.Name, fileType, uf.ID); err != nil {
			logctx.From(ctx).Error("failed to enqueue file processing job", "fileId", rec.ID, "error", err)
			h.removeLibraryBackedRow(ctx, rec.ID)
			return results, &libraryAddError{public: "failed to queue file for processing", err: err}
		}
		running.FileCount++
		running.TotalSize += uf.Size
		results = append(results, AddResult{UserFileID: uf.ID, FileID: rec.ID, Status: AddStatusAdded})
	}
	return results, nil
}
