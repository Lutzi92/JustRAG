package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/processor"
	"github.com/justrag/go-backend/internal/storage"
)

// URLProcessingPayload is an alias for jobs.URLProcessingPayload so that
// existing callers keep compiling during the migration.
type URLProcessingPayload = jobs.URLProcessingPayload

// NewURLProcessingHandler returns an asynq.HandlerFunc that processes a file
// previously saved to storage (e.g. a crawled URL). When using S3, the file
// is downloaded to a temp file; for local storage the path is used directly.
//
// queryCache is optional — when non-nil, the handler nukes cached
// SearchResults for the KB after a successful ingestion.
func NewURLProcessingHandler(proc *processor.Processor, stor storage.Storage, kbStore KBChunkConfigStore, queryCache QueryCacheInvalidator) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.URLProcessingPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal url processing payload: %w", err)
		}

		slog.Info("processing URL content",
			"fileId", payload.FileID,
			"kbId", payload.KbID,
			"filePath", payload.FilePath,
			"fileName", payload.OriginalName,
		)

		// For S3 storage download to a temp file; for local storage use the path directly.
		localPath := payload.FilePath
		var cleanup func()

		if stor != nil && stor.IsS3() {
			data, err := stor.ReadFile(ctx, payload.FilePath)
			if err != nil {
				return fmt.Errorf("download file from storage: %w", err)
			}
			tmpFile, err := os.CreateTemp("", "justrag-url-*")
			if err != nil {
				return fmt.Errorf("create temp file: %w", err)
			}
			if _, err := tmpFile.Write(data); err != nil {
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				return fmt.Errorf("write temp file: %w", err)
			}
			if err := tmpFile.Close(); err != nil {
				os.Remove(tmpFile.Name())
				return fmt.Errorf("close temp file: %w", err)
			}
			localPath = tmpFile.Name()
			cleanup = func() { os.Remove(tmpFile.Name()) }
		}

		if cleanup != nil {
			defer cleanup()
		}

		chunkSize, chunkOverlap := 0, 0
		if kbStore != nil && payload.KbID != "" {
			cs, co, err := kbStore.GetKBChunkConfig(ctx, payload.KbID)
			if err == nil {
				chunkSize, chunkOverlap = cs, co
			}
		}

		if err := proc.ProcessFile(ctx, processor.ProcessFileInput{
			FileID:       payload.FileID,
			FilePath:     localPath,
			FileName:     payload.OriginalName,
			MimeType:     payload.MimeType,
			KBID:         payload.KbID,
			ChunkSize:    chunkSize,
			ChunkOverlap: chunkOverlap,
		}); err != nil {
			slog.Error("URL processing failed",
				"fileId", payload.FileID,
				"error", err,
			)
			return err
		}

		invalidateKBQueryCache(ctx, queryCache, payload.KbID, "file_added")

		slog.Info("URL processing completed", "fileId", payload.FileID)
		return nil
	}
}
