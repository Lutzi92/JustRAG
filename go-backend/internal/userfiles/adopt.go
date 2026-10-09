package userfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/storage"
)

// Skip reasons of POST /api/kb/{id}/files/adopt (frontend contract). They are
// also the bounded outcome labels of rag_user_file_adopt_total.
const (
	AdoptSkipNotFound       = "not_found"
	AdoptSkipNotUpload      = "not_upload"
	AdoptSkipAlreadyLibrary = "already_library"
	AdoptSkipQuotaExceeded  = "quota_exceeded"
	AdoptSkipBlobMissing    = "blob_missing"
	// AdoptSkipBusy: the file is pending/processing; a queued or running
	// ingestion task carries the OLD storage path and would read a deleted blob.
	AdoptSkipBusy = "busy"
	// AdoptSkipDuplicateInKB: the KB already holds another copy of the same
	// library file (identical bytes uploaded twice); this row stays legacy.
	AdoptSkipDuplicateInKB = "duplicate_in_kb"

	// adoptCleanupTimeout bounds rollback and old-blob cleanup, which run on a
	// context detached from the request so a client disconnect cannot leave a
	// phantom user_files row or leak the legacy blob.
	adoptCleanupTimeout = 30 * time.Second
)

// isBusyStatus reports whether a files.status has (or may soon have) a worker
// reading the stored path.
func isBusyStatus(status string) bool { return status == "pending" || status == "processing" }

// LegacyFile is the slice of a files row adoption needs.
type LegacyFile struct {
	ID          string
	KBID        string
	Name        string
	Type        string
	Size        int64
	Origin      string
	Status      string
	StoragePath string
	UserFileID  string // "" when NULL
	UploadedBy  string // "" when NULL
}

// AdoptStore is the files-table side of adoption. PGAdoptStore implements it.
type AdoptStore interface {
	// GetLegacy returns the files row of kbID, or (nil, nil) when it does not
	// exist in that KB (or the id is malformed).
	GetLegacy(ctx context.Context, fileID, kbID string) (*LegacyFile, error)
	UserExists(ctx context.Context, userID string) (bool, error)
	// FindBySHA returns the owner's library row with that hash, or (nil, nil).
	FindBySHA(ctx context.Context, ownerID, sha string) (*UserFile, error)
	// LinkFile is the guarded UPDATE: it sets user_file_id/storage_path (and
	// uploaded_by when NULL) only while user_file_id IS NULL and the status is
	// not pending/processing, and reports whether a row changed. A KB that already holds a copy of the same
	// library file (unique index) also reports false.
	LinkFile(ctx context.Context, fileID, userFileID, storagePath, uploaderID string) (bool, error)
	// CountByStoragePath counts files rows referencing storagePath.
	CountByStoragePath(ctx context.Context, storagePath string) (int, error)
	// DeleteUnreferenced removes a user_files row no files row points at.
	DeleteUnreferenced(ctx context.Context, userFileID string) (bool, error)
}

// AdoptedFile is one successfully adopted file.
type AdoptedFile struct {
	FileID     string `json:"fileId"`
	UserFileID string `json:"userFileId"`
}

// AdoptSkipped is one file that was not adopted, with the reason.
type AdoptSkipped struct {
	FileID string `json:"fileId"`
	Reason string `json:"reason"`
}

// AdoptResult is the response body of the adopt endpoint.
type AdoptResult struct {
	Adopted []AdoptedFile  `json:"adopted"`
	Skipped []AdoptSkipped `json:"skipped"`
}

// Adopter re-keys legacy KB uploads (origin='upload', user_file_id NULL) into
// a user's library. The index (chunks, KG, tabular) is untouched: same
// files.id, same bytes.
type Adopter struct {
	files AdoptStore
	lib   Store
	stor  storage.Storage
	quota QuotaReader
}

func NewAdopter(files AdoptStore, lib Store, stor storage.Storage, quota QuotaReader) *Adopter {
	return &Adopter{files: files, lib: lib, stor: stor, quota: quota}
}

// Adopt processes fileIDs of kbID in order. Per-file problems are skips;
// only an unexpected infrastructure error aborts (files handled so far stay
// adopted and are not reported).
func (a *Adopter) Adopt(ctx context.Context, kbID, callerID string, fileIDs []string) (*AdoptResult, error) {
	res := &AdoptResult{Adopted: []AdoptedFile{}, Skipped: []AdoptSkipped{}}
	for _, id := range fileIDs {
		ufID, reason, err := a.adoptOne(ctx, kbID, callerID, id)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			observability.RecordUserFileAdopt(reason)
			res.Skipped = append(res.Skipped, AdoptSkipped{FileID: id, Reason: reason})
			continue
		}
		observability.RecordUserFileAdopt("adopted")
		res.Adopted = append(res.Adopted, AdoptedFile{FileID: id, UserFileID: ufID})
	}
	return res, nil
}

// hashBlob streams the blob at path through sha256. missing is true when the
// object does not exist.
func (a *Adopter) hashBlob(ctx context.Context, path string) (sum string, n int64, missing bool, err error) {
	ok, err := a.stor.FileExists(ctx, path)
	if err != nil {
		return "", 0, false, err
	}
	if !ok {
		return "", 0, true, nil
	}
	rc, err := a.stor.ReadFileStream(ctx, path)
	if err != nil {
		return "", 0, false, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err = io.Copy(h, rc)
	if err != nil {
		return "", 0, false, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, false, nil
}

func (a *Adopter) adoptOne(ctx context.Context, kbID, callerID, fileID string) (userFileID, skip string, err error) {
	lf, err := a.files.GetLegacy(ctx, fileID, kbID)
	if err != nil {
		return "", "", err
	}
	switch {
	case lf == nil:
		return "", AdoptSkipNotFound, nil
	case lf.Origin != "upload":
		return "", AdoptSkipNotUpload, nil
	case lf.UserFileID != "":
		return "", AdoptSkipAlreadyLibrary, nil
	case isBusyStatus(lf.Status):
		return "", AdoptSkipBusy, nil
	}

	target := callerID
	if lf.UploadedBy != "" {
		ok, err := a.files.UserExists(ctx, lf.UploadedBy)
		if err != nil {
			return "", "", err
		}
		if ok {
			target = lf.UploadedBy
		}
	}

	if lf.StoragePath == "" {
		// A legacy row without a blob key: FileExists("") on the local backend
		// would stat the data directory itself, so decide it here as a
		// per-file skip rather than a batch-wide error.
		return "", AdoptSkipBlobMissing, nil
	}
	sum, size, missing, err := a.hashBlob(ctx, lf.StoragePath)
	if err != nil {
		return "", "", fmt.Errorf("adopt: read legacy blob %s: %w", fileID, err)
	}
	if missing {
		return "", AdoptSkipBlobMissing, nil
	}

	var (
		uf      *UserFile
		newBlob string // set when this call wrote both a blob and a row
	)
	existing, err := a.files.FindBySHA(ctx, target, sum)
	if err != nil {
		return "", "", err
	}
	if existing != nil {
		// Same bytes already in the library: link, no copy, no quota change.
		uf = existing
	} else {
		over, err := a.overQuota(ctx, target, size)
		if err != nil {
			return "", "", err
		}
		if over {
			return "", AdoptSkipQuotaExceeded, nil
		}
		uf, newBlob, err = a.copyIntoLibrary(ctx, lf, target, sum, size)
		if err != nil {
			return "", "", err
		}
	}

	// Cleanup must survive a cancelled request context.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adoptCleanupTimeout)
	defer cancel()

	linked, err := a.files.LinkFile(ctx, lf.ID, uf.ID, uf.StoragePath, callerID)
	if err != nil {
		a.rollbackCreated(cctx, uf, newBlob)
		return "", "", fmt.Errorf("adopt: link %s: %w", fileID, err)
	}
	if !linked {
		// The guarded UPDATE matched nothing: drop what this call created so
		// nothing is orphaned, then classify why from the current row.
		a.rollbackCreated(cctx, uf, newBlob)
		return "", a.classifyLinkLoss(cctx, fileID, kbID), nil
	}

	a.deleteOldBlob(cctx, lf.StoragePath, uf.StoragePath)
	return uf.ID, "", nil
}

// classifyLinkLoss explains a 0-row guarded UPDATE: the row went busy
// (busy), was linked meanwhile (already_library), vanished (not_found), or is
// still unlinked and idle, i.e. the (kb_id, user_file_id) unique index
// refused because the KB already holds that library file (duplicate_in_kb).
func (a *Adopter) classifyLinkLoss(ctx context.Context, fileID, kbID string) string {
	cur, err := a.files.GetLegacy(ctx, fileID, kbID)
	switch {
	case err != nil || cur == nil:
		return AdoptSkipNotFound
	case cur.UserFileID != "":
		return AdoptSkipAlreadyLibrary
	case isBusyStatus(cur.Status):
		return AdoptSkipBusy
	}
	return AdoptSkipDuplicateInKB
}

func (a *Adopter) overQuota(ctx context.Context, owner string, add int64) (bool, error) {
	quota, err := EffectiveQuota(ctx, a.lib, a.quota, owner)
	if err != nil {
		return false, err
	}
	if quota <= 0 {
		return false, nil
	}
	used, err := a.lib.UsedBytes(ctx, owner)
	if err != nil {
		return false, err
	}
	return used+add > quota, nil
}

// copyIntoLibrary copies the legacy blob to users/<owner>/<new id> and inserts
// the library row. newBlob is the path this call owns and must clean up on a
// later failure; it is "" when Insert reported a dedup hit (the copy is
// already deleted) and uf is then the pre-existing row.
func (a *Adopter) copyIntoLibrary(ctx context.Context, lf *LegacyFile, owner, sum string, size int64) (uf *UserFile, newBlob string, err error) {
	id := uuid.NewString()
	path := "users/" + owner + "/" + id
	rc, err := a.stor.ReadFileStream(ctx, lf.StoragePath)
	if err != nil {
		return nil, "", fmt.Errorf("adopt: reopen legacy blob: %w", err)
	}
	defer rc.Close()
	h := sha256.New()
	if err := a.stor.StoreFileFromReader(ctx, path, io.TeeReader(rc, h), lf.Type); err != nil {
		_ = a.stor.DeleteFile(ctx, path)
		return nil, "", fmt.Errorf("adopt: copy blob: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		_ = a.stor.DeleteFile(ctx, path)
		return nil, "", errors.New("adopt: legacy blob changed while copying")
	}
	// filepath.Base: a library name is a bare file name, but the legacy
	// files.name column was never constrained, so strip any directory part
	// that a pre-library upload path may have left in it.
	name := filepath.Base(lf.Name)
	if name == "." || name == "/" || name == "" {
		name = "file" // an empty or separator-only legacy name
	}
	row, created, err := a.lib.Insert(ctx, NewUserFile{
		ID: id, OwnerUserID: owner, Name: name, Mime: lf.Type,
		Size: size, SHA256: sum, StoragePath: path,
	})
	if err != nil {
		a.delBlob(ctx, path)
		return nil, "", fmt.Errorf("adopt: insert library row: %w", err)
	}
	if !created {
		// Lost a race with another insert of the same bytes.
		a.delBlob(ctx, path)
		return row, "", nil
	}
	return row, path, nil
}

// rollbackCreated undoes what this call created. A pre-existing row
// (newBlob == "") is left alone.
func (a *Adopter) rollbackCreated(ctx context.Context, uf *UserFile, newBlob string) {
	if newBlob == "" {
		return
	}
	removed, err := a.files.DeleteUnreferenced(ctx, uf.ID)
	if err != nil {
		logctx.From(ctx).Warn("userfiles: adopt rollback: delete library row", "userFileId", uf.ID, "error", err)
		return // keep the blob: the row may still point at it
	}
	if removed {
		a.delBlob(ctx, newBlob)
	}
}

// deleteOldBlob removes the legacy blob unless another files row still
// references it. Best effort.
func (a *Adopter) deleteOldBlob(ctx context.Context, oldPath, newPath string) {
	if oldPath == "" || oldPath == newPath {
		return
	}
	n, err := a.files.CountByStoragePath(ctx, oldPath)
	if err != nil {
		logctx.From(ctx).Warn("userfiles: adopt: count old blob refs", "path", oldPath, "error", err)
		return
	}
	if n > 0 {
		return
	}
	a.delBlob(ctx, oldPath)
}

func (a *Adopter) delBlob(ctx context.Context, path string) {
	if err := a.stor.DeleteFile(ctx, path); err != nil {
		logctx.From(ctx).Warn("userfiles: adopt: delete blob", "path", path, "error", err)
	}
}
