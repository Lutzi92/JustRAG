package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/processor"
)

// kbScreeningFiles is the files-store surface the handler needs
// (satisfied by *files.PGStore).
type kbScreeningFiles interface {
	processor.ScreeningStore
	ListUnscreenedUserFiles(ctx context.Context, kbID string, origins []string) ([]files.UnscreenedFile, error)
}

// KBScreeningDeps are the injected dependencies. Text returns a file's
// stored leaf chunk text ("" when it has none) — production wires
// (*vector.ChunkService).GetFileLeafTextAllDims.
type KBScreeningDeps struct {
	Files  kbScreeningFiles
	Text   func(ctx context.Context, kbID, fileID string) (string, error)
	Reader processor.SiteConfigReader
}

// NewKBScreeningHandler screens a just-published KB's never-screened
// user-added files (user file library spec §11.2). Those files went in
// while the KB was private, where user content is not screened; once
// public they are third-party content for every reader.
//
// It reads stored chunk text rather than re-parsing: phase 0 has no parse
// cache, and a re-parse (Docling) per file would turn a publish click into
// an ingest-sized job. The recorded position is therefore an offset into
// the joined chunk text, not into the original parse.
//
// Per-file failures are logged and skipped; the task only errors on a bad
// payload or when the file list itself cannot be read.
func NewKBScreeningHandler(deps KBScreeningDeps) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var p jobs.KBScreeningPayload
		if err := json.Unmarshal(task.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal kb-screening payload: %w", err)
		}
		if p.KbID == "" {
			return fmt.Errorf("kb-screening: empty kbId")
		}
		if !processor.ScreeningEnabled(ctx, deps.Reader) {
			return nil
		}
		list, err := deps.Files.ListUnscreenedUserFiles(ctx, p.KbID, processor.PublicOnlyOrigins())
		if err != nil {
			return fmt.Errorf("kb-screening: list files: %w", err)
		}
		sheet := &parser.SpreadsheetParser{}
		screened := 0
		for _, f := range list {
			if sheet.CanParse(f.Type, f.Name) {
				continue
			}
			text, err := deps.Text(ctx, p.KbID, f.ID)
			if err != nil {
				logctx.From(ctx).Warn("kb-screening: read chunk text failed",
					"kb_id", p.KbID, "file_id", f.ID, "error", err)
				continue
			}
			if text == "" {
				continue // no chunks: recording "clean" would claim a check that never happened
			}
			processor.ScreenAndRecord(ctx, deps.Files, deps.Reader, f.ID, f.Origin, text)
			screened++
		}
		logctx.From(ctx).Info("kb-screening: done",
			"kb_id", p.KbID, "candidates", len(list), "screened", screened)
		return nil
	}
}
