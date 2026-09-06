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

	"github.com/justrag/go-backend/internal/files"
)

func TestSetAndClearInjectionFlag(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()
	_, fileID := seedErrorFile(t, pool, "processing")

	// A freshly inserted row is unflagged with a NULL detail: that pair is
	// what migration 0072 defines as "never screened", as opposed to
	// "screened and clean" (false flag, non-NULL detail).
	var flag bool
	var detail *string
	if err := pool.QueryRow(ctx,
		`SELECT injection_flag, injection_detail::text FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&flag, &detail); err != nil {
		t.Fatalf("read initial: %v", err)
	}
	if flag || detail != nil {
		t.Fatalf("fresh row: flag=%v detail=%v, want false/NULL", flag, detail)
	}

	payload := []byte(`{"rule":"ignore_previous","position":42,"snippet":"Ignore all previous instructions","screened_at":"2026-09-06T00:00:00Z"}`)
	if err := store.SetInjectionFlag(ctx, fileID, payload); err != nil {
		t.Fatalf("SetInjectionFlag: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT injection_flag, injection_detail::text FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&flag, &detail); err != nil {
		t.Fatalf("read after set: %v", err)
	}
	if !flag {
		t.Error("injection_flag must be true after SetInjectionFlag")
	}
	if detail == nil {
		t.Fatal("injection_detail must be written")
	}
	// The column is jsonb, so the round trip must survive as JSON — a
	// text/jsonb cast mistake would store the payload as a quoted string.
	var back struct {
		Rule     string `json:"rule"`
		Position int    `json:"position"`
		Snippet  string `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(*detail), &back); err != nil {
		t.Fatalf("injection_detail is not jsonb-readable JSON: %v (%s)", err, *detail)
	}
	if back.Rule != "ignore_previous" || back.Position != 42 {
		t.Errorf("round trip lost fields: %+v", back)
	}

	// A re-ingest that finds nothing must drop the badge, not keep it.
	if err := store.ClearInjectionFlag(ctx, fileID); err != nil {
		t.Fatalf("ClearInjectionFlag: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT injection_flag, injection_detail::text FROM files WHERE id = $1::uuid`, fileID,
	).Scan(&flag, &detail); err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	if flag || detail != nil {
		t.Errorf("after clear: flag=%v detail=%v, want false/NULL", flag, detail)
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
