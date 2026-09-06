//go:build integration

// Store-level tests for the ingest prompt-injection screening columns
// (files.injection_flag / injection_detail, migration 0072 — Wave-5 Task 6).
// Require a live main Postgres; skipped when DB_* env is unset. Pool/seed
// helpers are shared with store_pg_integration_test.go (same package).

package files_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/files"
)

// readVerdict returns the two screening columns as (flag, detail-or-nil).
func readVerdict(t *testing.T, pool *pgxpool.Pool, fileID string) (bool, map[string]any) {
	t.Helper()
	var flag bool
	var raw *string
	if err := pool.QueryRow(context.Background(),
		`SELECT injection_flag, injection_detail::text FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&flag, &raw); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if raw == nil {
		return flag, nil
	}
	var m map[string]any
	// The column is jsonb, so the round trip must survive as an OBJECT — a
	// missing ::jsonb cast would store the payload as a quoted string and
	// this unmarshal into a map is what catches it.
	if err := json.Unmarshal([]byte(*raw), &m); err != nil {
		t.Fatalf("injection_detail is not a JSON object: %v (%s)", err, *raw)
	}
	return flag, m
}

// TestInjectionVerdictThreeStates walks all three states of the
// (injection_flag, injection_detail) pair and the conditional no-op that
// keeps a poll from rewriting an already-clean row.
//
//	detail IS NULL          never screened
//	detail = {screened_at}  screened, clean
//	detail carries "rule"   screened, flagged
func TestInjectionVerdictThreeStates(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "processing")

	// State 1: never screened. A freshly inserted row must be false/NULL,
	// which is what an already-ingested corpus looks like after migration
	// 0072 (no backfill).
	flag, detail := readVerdict(t, pool, fileID)
	if flag || detail != nil {
		t.Fatalf("fresh row: flag=%v detail=%v, want false/NULL", flag, detail)
	}

	// State 2: screened, clean. The detail must carry screened_at and
	// NOTHING else — a rule or snippet here would read as a finding.
	firstClean := []byte(`{"screened_at":"2026-09-06T00:00:00Z"}`)
	if err := store.MarkInjectionScreenedClean(ctx, fileID, firstClean); err != nil {
		t.Fatalf("MarkInjectionScreenedClean: %v", err)
	}
	flag, detail = readVerdict(t, pool, fileID)
	if flag {
		t.Error("a clean pass must leave injection_flag false")
	}
	if detail == nil {
		t.Fatal("a clean pass must WRITE a detail — a NULL one is indistinguishable from never screened")
	}
	if len(detail) != 1 || detail["screened_at"] != "2026-09-06T00:00:00Z" {
		t.Errorf("clean detail = %v, want screened_at only", detail)
	}

	// The conditional: an already-clean row must NOT be rewritten. Every
	// RSS/Confluence/git poll re-ingests unchanged documents, so an
	// unconditional UPDATE would churn the files table on every sweep.
	secondClean := []byte(`{"screened_at":"2026-09-07T00:00:00Z"}`)
	if err := store.MarkInjectionScreenedClean(ctx, fileID, secondClean); err != nil {
		t.Fatalf("MarkInjectionScreenedClean (repeat): %v", err)
	}
	_, detail = readVerdict(t, pool, fileID)
	if detail["screened_at"] != "2026-09-06T00:00:00Z" {
		t.Errorf("an already-clean row must not be rewritten, got %v", detail)
	}

	// State 3: screened, flagged.
	payload := []byte(`{"rule":"ignore_previous","position":42,"snippet":"Ignore all previous instructions","screened_at":"2026-09-06T00:00:00Z"}`)
	if err := store.SetInjectionFlag(ctx, fileID, payload); err != nil {
		t.Fatalf("SetInjectionFlag: %v", err)
	}
	flag, detail = readVerdict(t, pool, fileID)
	if !flag {
		t.Error("injection_flag must be true after SetInjectionFlag")
	}
	if detail["rule"] != "ignore_previous" || detail["position"] != float64(42) {
		t.Errorf("round trip lost fields: %v", detail)
	}

	// Back to clean: a flagged row IS rewritten (first disjunct of the
	// WHERE clause), so a re-ingest drops the stale badge.
	if err := store.MarkInjectionScreenedClean(ctx, fileID, secondClean); err != nil {
		t.Fatalf("MarkInjectionScreenedClean (after flag): %v", err)
	}
	flag, detail = readVerdict(t, pool, fileID)
	if flag {
		t.Error("a clean re-screen must drop the flag")
	}
	if len(detail) != 1 || detail["screened_at"] != "2026-09-07T00:00:00Z" {
		t.Errorf("after re-screen: detail = %v, want screened_at only", detail)
	}

	// And a row carrying an old finding but a false flag (a hand-fixed row)
	// is rewritten too — the third disjunct.
	if _, err := pool.Exec(ctx,
		`UPDATE files SET injection_flag = false, injection_detail = $1::jsonb WHERE id = $2::uuid`,
		payload, fileID); err != nil {
		t.Fatalf("seed stale finding: %v", err)
	}
	if err := store.MarkInjectionScreenedClean(ctx, fileID, firstClean); err != nil {
		t.Fatalf("MarkInjectionScreenedClean (stale finding): %v", err)
	}
	_, detail = readVerdict(t, pool, fileID)
	if len(detail) != 1 || detail["screened_at"] != "2026-09-06T00:00:00Z" {
		t.Errorf("a stale finding must be replaced, got %v", detail)
	}
}

func TestGetFileOrigin(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "completed")

	// seedErrorFile does not set origin, so the column default applies —
	// which is exactly the value the screen must see for a plain upload.
	got, err := store.GetFileOrigin(ctx, fileID)
	if err != nil {
		t.Fatalf("GetFileOrigin: %v", err)
	}
	if got != "upload" {
		t.Errorf("origin = %q, want upload (the files.origin default)", got)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE files SET origin = 'rss' WHERE id = $1::uuid`, fileID); err != nil {
		t.Fatalf("set origin: %v", err)
	}
	got, err = store.GetFileOrigin(ctx, fileID)
	if err != nil {
		t.Fatalf("GetFileOrigin after update: %v", err)
	}
	if got != "rss" {
		t.Errorf("origin = %q, want rss", got)
	}

	// A missing row is "", nil — never an error: a file deleted mid-ingest
	// must not turn into a failed ingestion.
	got, err = store.GetFileOrigin(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetFileOrigin(missing): unexpected error %v", err)
	}
	if got != "" {
		t.Errorf("missing file: origin = %q, want \"\"", got)
	}
}
