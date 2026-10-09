package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/processor"
	"github.com/justrag/go-backend/internal/storage"
)

// FileProcessingPayload is an alias for jobs.FileProcessingPayload so that
// existing callers (e.g. files package) keep compiling during the migration.
type FileProcessingPayload = jobs.FileProcessingPayload

// KBChunkConfigStore looks up per-KB chunk settings.
type KBChunkConfigStore interface {
	GetKBChunkConfig(ctx context.Context, kbID string) (chunkSize, chunkOverlap int, err error)
}

// QueryCacheInvalidator nukes cached SearchResults for a KB when the
// underlying chunk inventory changes (ingestion completion, re-ingest).
// Wired through *vector.SearchService in production. Optional — a nil
// invalidator skips the call (the TTL fallback eventually corrects any
// missed invalidation).
type QueryCacheInvalidator interface {
	InvalidateQueryCache(ctx context.Context, kbID string) error
}

// invalidateKBQueryCache fires the optional query-cache invalidation
// hook. Fail-safe: nil invalidator is a no-op; errors are logged but
// never propagated, since the cache is best-effort.
func invalidateKBQueryCache(ctx context.Context, qc QueryCacheInvalidator, kbID, reason string) {
	if qc == nil || kbID == "" {
		return
	}
	if err := qc.InvalidateQueryCache(ctx, kbID); err != nil {
		slog.Warn("query_cache: invalidate failed",
			"kb_id", kbID, "reason", reason, "error", err)
	}
}

// fileProcessor is the part of *processor.Processor the handler calls.
type fileProcessor interface {
	ProcessFileWithResult(ctx context.Context, in processor.ProcessFileInput) (processor.ProcessOutcome, error)
}

// OwnerLookup resolves the owner of a user-library file. The worker uses it to
// key the parse cache; a failed lookup just disables the cache for the task.
type OwnerLookup interface {
	UserFileOwner(ctx context.Context, userFileID string) (string, error)
}

// NewFileProcessingHandler returns an asynq.HandlerFunc that processes a single file.
// It looks up the KB's chunk settings before processing.
// If storage is S3, the file is downloaded to a temp path first.
//
// queryCache is optional — when non-nil, the handler nukes cached
// SearchResults for the KB after a successful ingestion so freshly
// embedded chunks are visible immediately rather than after the cache TTL.
func NewFileProcessingHandler(proc *processor.Processor, kbStore KBChunkConfigStore, queryCache QueryCacheInvalidator, stor ...storage.Storage) asynq.HandlerFunc {
	return NewFileProcessingHandlerWithOwners(proc, kbStore, queryCache, nil, stor...)
}

// NewFileProcessingHandlerWithOwners is NewFileProcessingHandler plus the
// owner lookup that enables the library parse cache. owners may be nil.
func NewFileProcessingHandlerWithOwners(proc fileProcessor, kbStore KBChunkConfigStore, queryCache QueryCacheInvalidator, owners OwnerLookup, stor ...storage.Storage) asynq.HandlerFunc {
	var storageBackend storage.Storage
	if len(stor) > 0 {
		storageBackend = stor[0]
	}
	return NewFileProcessingHandlerWithDeps(FileProcessingDeps{
		Proc:       proc,
		KBStore:    kbStore,
		QueryCache: queryCache,
		Owners:     owners,
		Storage:    storageBackend,
	})
}

// FileProcessingDeps wires the file-processing handler. Everything but Proc
// is optional; a nil Copy disables copy mode (every payload ingests).
type FileProcessingDeps struct {
	Proc       fileProcessor
	KBStore    KBChunkConfigStore
	QueryCache QueryCacheInvalidator
	Owners     OwnerLookup
	// Links, when set, resolves the library link (user file + owner) from
	// the files row and supersedes the payload's UserFileID and Owners.
	Links   LibraryLinkLookup
	Storage storage.Storage
	Copy    *CopyDeps
}

// LibraryLinkLookup reads a KB file's library link from its files row: the
// user_file_id and the library file's owner, both "" for a non-library file.
// *files.PGStore satisfies it.
type LibraryLinkLookup interface {
	LibraryLink(ctx context.Context, fileID string) (userFileID, ownerUserID string, err error)
}

// CurrentPathLookup reads files.storage_path. Optionally implemented by the
// Links value (*files.PGStore does).
type CurrentPathLookup interface {
	CurrentStoragePath(ctx context.Context, fileID string) (string, error)
}

// ResolveCurrentPath returns the path a task should read: the files row's
// current storage_path when it is non-empty (adoption may have moved the blob
// since enqueue), else payload.FilePath. A lookup error keeps payload.FilePath.
func ResolveCurrentPath(ctx context.Context, lookup any, payload jobs.FileProcessingPayload) string {
	cp, ok := lookup.(CurrentPathLookup)
	if !ok || payload.FileID == "" {
		return payload.FilePath
	}
	cur, err := cp.CurrentStoragePath(ctx, payload.FileID)
	if err != nil {
		slog.Warn("current storage path lookup failed; using payload path", "fileId", payload.FileID, "error", err)
		return payload.FilePath
	}
	if cur != "" && cur != payload.FilePath {
		slog.Info("storage path changed since enqueue; using current path",
			"fileId", payload.FileID, "payloadPath", payload.FilePath, "currentPath", cur)
		return cur
	}
	return payload.FilePath
}

// PreflightReembed fails when the blob a re-embed would read is gone, so the
// caller can abort BEFORE deleting the working index.
func PreflightReembed(ctx context.Context, lookup any, stor storage.Storage, payload jobs.FileProcessingPayload) error {
	path := ResolveCurrentPath(ctx, lookup, payload)
	if path == "" {
		return fmt.Errorf("re-embedding: file %s has no storage path", payload.FileID)
	}
	ok, err := stor.FileExists(ctx, path)
	if err != nil {
		return fmt.Errorf("re-embedding: check blob for file %s: %w", payload.FileID, err)
	}
	if !ok {
		return fmt.Errorf("re-embedding: blob %q of file %s is missing; index left untouched", path, payload.FileID)
	}
	return nil
}

// resolveLibraryLink sets payload.UserFileID and returns the owner. With
// links, both come from the files row, not the payload: re-embeds and the
// per-file retry endpoint enqueue payloads without UserFileID, and a library
// copy must still re-stamp its fingerprint and use the parse/KG caches. A
// failed lookup proceeds as a non-library file. Without links the payload's
// UserFileID is used and only the owner is read (owners may be nil); a failed
// owner lookup only costs the parse cache.
func resolveLibraryLink(ctx context.Context, links LibraryLinkLookup, owners OwnerLookup, payload *jobs.FileProcessingPayload) string {
	if links != nil {
		uf, owner, err := links.LibraryLink(ctx, payload.FileID)
		if err != nil {
			slog.Warn("library link lookup failed; processing as a non-library file",
				"fileId", payload.FileID, "error", err)
			uf, owner = "", ""
		}
		payload.UserFileID = uf
		return owner
	}
	if payload.UserFileID == "" || owners == nil {
		return ""
	}
	o, err := owners.UserFileOwner(ctx, payload.UserFileID)
	if err != nil {
		slog.Warn("parse cache disabled: owner lookup failed",
			"fileId", payload.FileID, "userFileId", payload.UserFileID, "error", err)
		return ""
	}
	return o
}

// NewFileProcessingHandlerWithDeps is the full constructor: on top of the
// ingest path it can serve a library-backed file by copying another KB
// copy's index (copy mode, P2-R6) when deps.Copy is set.
func NewFileProcessingHandlerWithDeps(deps FileProcessingDeps) asynq.HandlerFunc {
	proc, kbStore, queryCache, owners, storageBackend := deps.Proc, deps.KBStore, deps.QueryCache, deps.Owners, deps.Storage
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.FileProcessingPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal file processing payload: %w", err)
		}

		slog.Info("processing file",
			"fileId", payload.FileID,
			"kbId", payload.KbID,
			"fileName", payload.OriginalName,
		)

		// Look up KB-specific chunk settings (0 means use defaults)
		chunkSize, chunkOverlap := 0, 0
		if kbStore != nil && payload.KbID != "" {
			cs, co, err := kbStore.GetKBChunkConfig(ctx, payload.KbID)
			if err == nil {
				chunkSize, chunkOverlap = cs, co
			}
		}

		ownerID := resolveLibraryLink(ctx, deps.Links, owners, &payload)
		if deps.Links != nil {
			payload.FilePath = ResolveCurrentPath(ctx, deps.Links, payload)
		}
		reembed := task.Type() == jobs.TypeReEmbedding

		// Copy mode (P2-R6): a library file whose index another KB copy
		// already built under the same fingerprint is copied server-side
		// instead of re-ingested. Decided here, at task time, before the
		// blob is downloaded. Any miss or failure falls through to ingest.
		// Re-embeds always ingest; spreadsheets, images and audio are never
		// copied (processor.CopyEligible).
		if deps.Copy != nil && !reembed && payload.UserFileID != "" && payload.KbID != "" &&
			processor.CopyEligible(payload.MimeType, payload.OriginalName) {
			copied, cerr := deps.Copy.tryCopy(ctx, payload, chunkSize, chunkOverlap, ownerID)
			if cerr != nil {
				slog.Error("file processing failed", "fileId", payload.FileID, "mode", "copy", "error", cerr)
				return cerr
			}
			if copied {
				invalidateKBQueryCache(ctx, queryCache, payload.KbID, "file_added")
				slog.Info("file processing completed", "fileId", payload.FileID, "mode", "copy")
				return nil
			}
		}

		// If storage is S3, stream the file to a temp path for local processing.
		// Uses ReadFileStream to avoid buffering the entire file in memory.
		localPath := payload.FilePath
		if storageBackend != nil && storageBackend.IsS3() {
			stream, dlErr := storageBackend.ReadFileStream(ctx, payload.FilePath)
			if dlErr != nil {
				return fmt.Errorf("download file from storage: %w", dlErr)
			}
			tmpFile, tmpErr := os.CreateTemp("", "justrag-file-*")
			if tmpErr != nil {
				stream.Close()
				return fmt.Errorf("create temp file: %w", tmpErr)
			}
			defer os.Remove(tmpFile.Name())
			if _, wErr := io.Copy(tmpFile, stream); wErr != nil {
				stream.Close()
				tmpFile.Close()
				return fmt.Errorf("write temp file: %w", wErr)
			}
			stream.Close()
			tmpFile.Close()
			localPath = tmpFile.Name()
		}

		outcome, err := proc.ProcessFileWithResult(ctx, processor.ProcessFileInput{
			FileID:       payload.FileID,
			FilePath:     localPath,
			FileName:     payload.OriginalName,
			MimeType:     payload.MimeType,
			KBID:         payload.KbID,
			ChunkSize:    chunkSize,
			ChunkOverlap: chunkOverlap,
			UserFileID:   payload.UserFileID,
			OwnerUserID:  ownerID,
		})
		if err != nil {
			slog.Error("file processing failed",
				"fileId", payload.FileID,
				"error", err,
			)
			return err
		}

		// A re-embed is not an add: only add tasks feed the add-mode metric.
		if payload.UserFileID != "" && !reembed {
			if outcome.ParseCacheHit {
				observability.RecordUserFileAdd("ingest_cached_parse")
			} else {
				observability.RecordUserFileAdd("ingest")
			}
		}

		// Ingestion success — nuke any cached SearchResults for this KB so
		// freshly embedded chunks are visible to subsequent searches without
		// waiting for the cache TTL. Best-effort: failures log and continue.
		invalidateKBQueryCache(ctx, queryCache, payload.KbID, "file_added")

		slog.Info("file processing completed", "fileId", payload.FileID)
		return nil
	}
}
