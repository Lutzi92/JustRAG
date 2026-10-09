package userfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/uploadcheck"
)

// QuotaReader reads the global default (site_config user_file_quota_bytes).
type QuotaReader interface {
	GlobalQuotaBytes(ctx context.Context) int64 // 0 = unlimited
}

// EffectiveQuota returns the per-user override if one is set, else the
// global default; 0 means unlimited.
func EffectiveQuota(ctx context.Context, store Store, reader QuotaReader, ownerID string) (int64, error) {
	ov, err := store.QuotaOverride(ctx, ownerID)
	if err != nil {
		return 0, err
	}
	if ov != nil {
		return *ov, nil
	}
	if reader == nil {
		return 0, nil
	}
	return reader.GlobalQuotaBytes(ctx), nil
}

// QuotaError is returned by Ingest when the upload would exceed the quota.
type QuotaError struct{ UsedBytes, QuotaBytes int64 }

func (e *QuotaError) Error() string {
	return fmt.Sprintf("library quota exceeded: %d of %d bytes used", e.UsedBytes, e.QuotaBytes)
}

// Ingester stores an uploaded file as a library file (blob + row, dedup,
// quota). Used by the library upload endpoint and by the rerouted KB upload.
// On dedup the just-written blob is deleted and the existing row returned
// with created=false. Quota is checked BEFORE the blob is written (size is
// known from the multipart header).
type Ingester struct {
	store Store
	stor  storage.Storage
	quota QuotaReader
}

func NewIngester(store Store, stor storage.Storage, quota QuotaReader) *Ingester {
	return &Ingester{store: store, stor: stor, quota: quota}
}

// Ingest stores up as a library file of ownerID. The caller still owns (and
// closes) up.File.
func (in *Ingester) Ingest(ctx context.Context, ownerID string, up *uploadcheck.Upload) (*UserFile, bool, error) {
	size := up.Header.Size

	quota, err := EffectiveQuota(ctx, in.store, in.quota, ownerID)
	if err != nil {
		return nil, false, err
	}
	if quota > 0 {
		used, err := in.store.UsedBytes(ctx, ownerID)
		if err != nil {
			return nil, false, err
		}
		if used+size > quota {
			return nil, false, &QuotaError{UsedBytes: used, QuotaBytes: quota}
		}
	}

	id := uuid.NewString()
	path := "users/" + ownerID + "/" + id
	h := sha256.New()
	if err := in.stor.StoreFileFromReader(ctx, path, io.TeeReader(up.File, h), up.MimeType); err != nil {
		// Best effort: a partial write must not leak an orphan blob.
		_ = in.stor.DeleteFile(ctx, path)
		return nil, false, fmt.Errorf("store library blob: %w", err)
	}

	row, created, err := in.store.Insert(ctx, NewUserFile{
		ID:          id,
		OwnerUserID: ownerID,
		Name:        filepath.Base(up.Header.Filename),
		Mime:        up.MimeType,
		Size:        size,
		SHA256:      hex.EncodeToString(h.Sum(nil)),
		StoragePath: path,
	})
	if err != nil {
		if delErr := in.stor.DeleteFile(ctx, path); delErr != nil {
			logctx.From(ctx).Warn("userfiles: delete blob after failed insert", "path", path, "error", delErr)
		}
		return nil, false, err
	}
	if !created {
		if delErr := in.stor.DeleteFile(ctx, path); delErr != nil {
			logctx.From(ctx).Warn("userfiles: delete duplicate blob", "path", path, "error", delErr)
		}
	}
	return row, created, nil
}
