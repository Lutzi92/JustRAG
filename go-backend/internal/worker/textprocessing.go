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
)

// TextProcessingPayload is an alias for jobs.TextProcessingPayload so that
// existing callers keep compiling during the migration.
type TextProcessingPayload = jobs.TextProcessingPayload

// NewTextProcessingHandler returns an asynq.HandlerFunc that processes inline text content.
// It writes the content to a temp file, calls the processor, then cleans up.
//
// queryCache is optional — when non-nil, the handler nukes cached
// SearchResults for the KB after a successful ingestion.
func NewTextProcessingHandler(proc *processor.Processor, kbStore KBChunkConfigStore, queryCache QueryCacheInvalidator) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.TextProcessingPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal text processing payload: %w", err)
		}

		slog.Info("processing text content",
			"fileId", payload.FileID,
			"kbId", payload.KbID,
			"fileName", payload.OriginalName,
		)

		// Write inline content to a temp file so the processor can handle it uniformly.
		tmpFile, err := os.CreateTemp("", "justrag-text-*")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.WriteString(payload.Content); err != nil {
			tmpFile.Close()
			return fmt.Errorf("write temp file: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("close temp file: %w", err)
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
			FilePath:     tmpFile.Name(),
			FileName:     payload.OriginalName,
			MimeType:     payload.MimeType,
			KBID:         payload.KbID,
			ChunkSize:    chunkSize,
			ChunkOverlap: chunkOverlap,
		}); err != nil {
			slog.Error("text processing failed",
				"fileId", payload.FileID,
				"error", err,
			)
			return err
		}

		invalidateKBQueryCache(ctx, queryCache, payload.KbID, "file_added")

		slog.Info("text processing completed", "fileId", payload.FileID)
		return nil
	}
}
