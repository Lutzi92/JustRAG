//go:build integration

// Store-level tests for the file error-detail columns (migration 0054).
// Require a live main Postgres; skipped when DB_* env is unset.

package files_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/store"
	"github.com/justrag/go-backend/internal/tabular"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("file error-store tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedErrorFile inserts a global KB (user_id NULL is valid for global KBs)
// and one file row, returning both ids. Rows are cleaned up via t.Cleanup.
func seedErrorFile(t *testing.T, pool *pgxpool.Pool, status string) (kbID, fileID string) {
	t.Helper()
	ctx := context.Background()
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility)
		VALUES ('file-error-store-test', 'fixture', 'public')
		RETURNING id::text`).Scan(&kbID)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})
	err = pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path)
		VALUES ($1::uuid, 'doc.pdf', 'application/pdf', $2, 'u/k/doc.pdf')
		RETURNING id::text`, kbID, status).Scan(&fileID)
	if err != nil {
		t.Fatalf("insert file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, fileID) //nolint:errcheck
	})
	return kbID, fileID
}

func readErrorFields(t *testing.T, pool *pgxpool.Pool, fileID string) (status string, stage, msg *string) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT status, error_stage, error_message FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&status, &stage, &msg)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return status, stage, msg
}

func TestMarkFileErrorAndClear(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "processing")

	if err := store.MarkFileError(ctx, fileID, "parse", "The file could not be parsed"); err != nil {
		t.Fatalf("MarkFileError: %v", err)
	}
	status, stage, msg := readErrorFields(t, pool, fileID)
	if status != "error" || stage == nil || *stage != "parse" || msg == nil || *msg != "The file could not be parsed" {
		t.Fatalf("after MarkFileError: status=%s stage=%v msg=%v", status, stage, msg)
	}

	// A second MarkFileError must overwrite the previous detail — this is
	// the contract that distinguishes it from MarkFileErrorIfUnset.
	if err := store.MarkFileError(ctx, fileID, "embedding", "Embedding service unavailable"); err != nil {
		t.Fatalf("MarkFileError(overwrite): %v", err)
	}
	if _, stage, msg := readErrorFields(t, pool, fileID); stage == nil || *stage != "embedding" || msg == nil || *msg != "Embedding service unavailable" {
		t.Fatalf("MarkFileError did not overwrite: stage=%v msg=%v", stage, msg)
	}

	// IfUnset must NOT clobber the recorded reason.
	if err := store.MarkFileErrorIfUnset(ctx, fileID, "processing", "Processing failed after 3 attempts"); err != nil {
		t.Fatalf("MarkFileErrorIfUnset: %v", err)
	}
	if _, stage, _ := readErrorFields(t, pool, fileID); stage == nil || *stage != "embedding" {
		t.Fatalf("IfUnset clobbered stage: %v", stage)
	}

	// Any non-error status transition clears the detail.
	if err := store.UpdateFileStatus(ctx, fileID, "completed"); err != nil {
		t.Fatalf("UpdateFileStatus: %v", err)
	}
	status, stage, msg = readErrorFields(t, pool, fileID)
	if status != "completed" || stage != nil || msg != nil {
		t.Fatalf("UpdateFileStatus did not clear: status=%s stage=%v msg=%v", status, stage, msg)
	}

	// IfUnset on a successfully-terminal file is a no-op — a late-firing
	// exhaustion wrapper must not regress a completed ingest.
	if err := store.MarkFileErrorIfUnset(ctx, fileID, "processing", "Processing failed after 3 attempts"); err != nil {
		t.Fatalf("MarkFileErrorIfUnset(completed): %v", err)
	}
	status, stage, msg = readErrorFields(t, pool, fileID)
	if status != "completed" || stage != nil || msg != nil {
		t.Fatalf("IfUnset must not touch a completed file: status=%s stage=%v msg=%v", status, stage, msg)
	}

	// IfUnset on a non-terminal row with NULL fields fills them.
	if err := store.UpdateFileStatus(ctx, fileID, "processing"); err != nil {
		t.Fatalf("UpdateFileStatus(processing): %v", err)
	}
	if err := store.MarkFileErrorIfUnset(ctx, fileID, "processing", "Processing failed after 3 attempts"); err != nil {
		t.Fatalf("MarkFileErrorIfUnset(2): %v", err)
	}
	status, stage, msg = readErrorFields(t, pool, fileID)
	if status != "error" || stage == nil || *stage != "processing" {
		t.Fatalf("IfUnset on NULL did not fill stage: status=%s stage=%v msg=%v", status, stage, msg)
	}
}

func TestResetFileForRetry(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "error")

	// Fix round 1, item 5: a retry must clear the PREVIOUS attempt's parse
	// report and stage detail too, not just error_stage/error_message —
	// otherwise a retry that fails again before reaching the spreadsheet
	// ingester (e.g. a parse-stage error) would leave a stale report/detail
	// visible as if it described the current attempt.
	if _, err := pool.Exec(ctx,
		`UPDATE files SET parse_report = '{"version":1}'::jsonb, stage_detail = 'Blatt 2/3' WHERE id = $1::uuid`,
		fileID); err != nil {
		t.Fatalf("seed parse_report/stage_detail: %v", err)
	}

	reset, err := store.ResetFileForRetry(ctx, fileID)
	if err != nil || !reset {
		t.Fatalf("first reset: reset=%v err=%v", reset, err)
	}
	status, stage, msg := readErrorFields(t, pool, fileID)
	if status != "pending" || stage != nil || msg != nil {
		t.Fatalf("after reset: status=%s stage=%v msg=%v", status, stage, msg)
	}
	var parseReport, stageDetail *string
	if err := pool.QueryRow(ctx,
		`SELECT parse_report::text, stage_detail FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&parseReport, &stageDetail); err != nil {
		t.Fatalf("read parse_report/stage_detail: %v", err)
	}
	if parseReport != nil || stageDetail != nil {
		t.Errorf("reset must clear parse_report/stage_detail: parseReport=%v stageDetail=%v", parseReport, stageDetail)
	}

	// Second reset loses the WHERE status='error' race — the 409 path.
	reset, err = store.ResetFileForRetry(ctx, fileID)
	if err != nil || reset {
		t.Fatalf("second reset should be a no-op: reset=%v err=%v", reset, err)
	}
}

func readStage(t *testing.T, pool *pgxpool.Pool, fileID string) (stage *string, idx, total *int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT current_stage, stage_index, stage_total FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&stage, &idx, &total); err != nil {
		t.Fatalf("readStage: %v", err)
	}
	return
}

func TestUpdateAndClearFileStage(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "processing")

	// Backdate progress_updated_at so the stuck-file sweep would consider this
	// file timed out; a stage transition must refresh it (liveness heartbeat).
	if _, err := pool.Exec(ctx, `UPDATE files SET progress_updated_at = NOW() - interval '2 hours' WHERE id = $1::uuid`, fileID); err != nil {
		t.Fatalf("backdate progress_updated_at: %v", err)
	}

	if err := store.UpdateFileStage(ctx, fileID, "embed", 3, 5); err != nil {
		t.Fatalf("UpdateFileStage: %v", err)
	}
	stage, idx, total := readStage(t, pool, fileID)
	if stage == nil || *stage != "embed" || idx == nil || *idx != 3 || total == nil || *total != 5 {
		t.Fatalf("got stage=%v idx=%v total=%v, want embed/3/5", stage, idx, total)
	}

	// progress_updated_at must have been refreshed to ~now, not left stale.
	var stale bool
	if err := pool.QueryRow(ctx, `SELECT progress_updated_at < NOW() - interval '1 minute' FROM files WHERE id = $1::uuid`, fileID).Scan(&stale); err != nil {
		t.Fatalf("read progress_updated_at: %v", err)
	}
	if stale {
		t.Fatalf("UpdateFileStage did not refresh progress_updated_at; stuck-file sweep would falsely time out an active file")
	}

	if err := store.ClearFileStage(ctx, fileID); err != nil {
		t.Fatalf("ClearFileStage: %v", err)
	}
	stage, idx, total = readStage(t, pool, fileID)
	if stage != nil || idx != nil || total != nil {
		t.Fatalf("after clear got stage=%v idx=%v total=%v, want all nil", stage, idx, total)
	}
}

func TestListErrorFiles(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	kbID, errFileID := seedErrorFile(t, pool, "error")

	// A completed file in the same KB must not be listed.
	var okFileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path)
		VALUES ($1::uuid, 'ok.pdf', 'application/pdf', 'completed', 'u/k/ok.pdf')
		RETURNING id::text`, kbID).Scan(&okFileID); err != nil {
		t.Fatalf("insert ok file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, okFileID) //nolint:errcheck
	})

	got, err := store.ListErrorFiles(ctx, kbID)
	if err != nil {
		t.Fatalf("ListErrorFiles: %v", err)
	}
	if len(got) != 1 || got[0].ID != errFileID {
		t.Fatalf("want exactly the error file %s, got %+v", errFileID, got)
	}
	if got[0].StoragePath == nil || *got[0].StoragePath == "" || got[0].KbID != kbID {
		t.Fatalf("FileInfo incomplete: %+v", got[0])
	}
}

// TestGetFileParseReport pins its three-way contract: NULL parse_report
// (a non-spreadsheet file, or one not yet materialised) reads back as
// (nil, nil) — not an error — a populated report round-trips byte-for-byte,
// and an unknown file id is store.ErrNotFound, distinct from the NULL case.
func TestGetFileParseReport(t *testing.T) {
	pool := openMainPool(t)
	fstore := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "completed")

	report, err := fstore.GetFileParseReport(ctx, fileID)
	if err != nil {
		t.Fatalf("GetFileParseReport (NULL): %v", err)
	}
	if report != nil {
		t.Fatalf("expected nil report for an un-set column, got %s", report)
	}

	want := `{"version":1,"materialised":true,"sheets":[{"name":"Sheet1"}]}`
	if err := fstore.SetFileParseReport(ctx, fileID, []byte(want)); err != nil {
		t.Fatalf("SetFileParseReport: %v", err)
	}
	report, err = fstore.GetFileParseReport(ctx, fileID)
	if err != nil {
		t.Fatalf("GetFileParseReport (set): %v", err)
	}
	// Compare semantically, not byte-for-byte: Postgres' jsonb re-serializes
	// (key order, whitespace) rather than storing the literal input bytes.
	var gotReport, wantReport tabular.ParseReport
	if err := json.Unmarshal(report, &gotReport); err != nil {
		t.Fatalf("decode got report: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantReport); err != nil {
		t.Fatalf("decode want report: %v", err)
	}
	if !reflect.DeepEqual(gotReport, wantReport) {
		t.Fatalf("got %+v, want %+v", gotReport, wantReport)
	}

	if _, err := fstore.GetFileParseReport(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected store.ErrNotFound for an unknown file id, got %v", err)
	}
}

// TestListSpreadsheetFiles pins the tabular-rematerialize endpoint's file
// selection: only spreadsheet extensions, matched case-insensitively, come
// back — a PDF in the same completed-status KB must not.
func TestListSpreadsheetFiles(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, visibility)
		VALUES ('list-spreadsheet-files-test', 'fixture', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	var xlsxID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path)
		VALUES ($1::uuid, 'Budget.XLSX', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'completed', 'u/k/budget.xlsx')
		RETURNING id::text`, kbID).Scan(&xlsxID); err != nil {
		t.Fatalf("insert xlsx file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, xlsxID) //nolint:errcheck
	})

	var pdfID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path)
		VALUES ($1::uuid, 'report.pdf', 'application/pdf', 'completed', 'u/k/report.pdf')
		RETURNING id::text`, kbID).Scan(&pdfID); err != nil {
		t.Fatalf("insert pdf file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, pdfID) //nolint:errcheck
	})

	got, err := store.ListSpreadsheetFiles(ctx, kbID)
	if err != nil {
		t.Fatalf("ListSpreadsheetFiles: %v", err)
	}
	if len(got) != 1 || got[0].ID != xlsxID {
		t.Fatalf("want exactly the xlsx file %s, got %+v", xlsxID, got)
	}
	if got[0].StoragePath == nil || *got[0].StoragePath == "" || got[0].KbID != kbID {
		t.Fatalf("FileInfo incomplete: %+v", got[0])
	}
}
