//go:build integration

// Integration test for ListFiles' exposure of the ingest prompt-injection
// screening verdict (files.injection_flag / injection_detail, migration
// 0072 — Wave-5 Task 6). The unit tests mock the store, so only a live
// query can catch a column missing from the SELECT list or a db tag that
// does not match. Skipped when DB_* env is unset; testPool/seed helpers are
// shared with store_pg_integration_test.go (same package).

package kb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kb"
)

// seedInjectionKB creates a public KB with two files: one flagged with a
// full detail payload, one untouched. Both are cleaned up by id.
func seedInjectionKB(t *testing.T, pool *pgxpool.Pool) (kbID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('kb-injection-listfiles', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path, origin, injection_flag, injection_detail, created_at)
		VALUES ($1::uuid, 'flagged.md', 'text/markdown', 'completed', 'p/flagged.md', 'rss', true,
		        '{"rule":"ignore_previous","position":42,"snippet":"Ignore all previous instructions","screened_at":"2026-09-06T00:00:00Z"}'::jsonb,
		        NOW())`, kbID); err != nil {
		t.Fatalf("seed flagged file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path, origin, created_at)
		VALUES ($1::uuid, 'clean.md', 'text/markdown', 'completed', 'p/clean.md', 'upload', NOW() - INTERVAL '1 hour')`,
		kbID); err != nil {
		t.Fatalf("seed clean file: %v", err)
	}
	return kbID
}

func TestListFilesExposesInjectionVerdict(t *testing.T) {
	pool := testPool(t)
	kbID := seedInjectionKB(t, pool)

	rows, total, err := kb.NewStore(pool).ListFiles(context.Background(), kbID, 50, 0)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}

	byName := map[string]kb.FileRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	flagged, ok := byName["flagged.md"]
	if !ok {
		t.Fatal("flagged.md missing from the page")
	}
	if !flagged.InjectionFlag {
		t.Error("InjectionFlag must be true for the seeded flagged file")
	}
	if len(flagged.InjectionDetail) == 0 {
		t.Fatal("InjectionDetail must carry the jsonb payload")
	}
	var detail struct {
		Rule     string `json:"rule"`
		Position int    `json:"position"`
		Snippet  string `json:"snippet"`
	}
	if err := json.Unmarshal(flagged.InjectionDetail, &detail); err != nil {
		t.Fatalf("InjectionDetail is not valid JSON: %v (%s)", err, flagged.InjectionDetail)
	}
	if detail.Rule != "ignore_previous" || detail.Position != 42 {
		t.Errorf("detail lost fields: %+v", detail)
	}

	clean, ok := byName["clean.md"]
	if !ok {
		t.Fatal("clean.md missing from the page")
	}
	if clean.InjectionFlag {
		t.Error("an unscreened upload must come back unflagged")
	}
	// NULL detail must marshal away entirely (json:"...,omitempty"), so the
	// frontend can branch on presence rather than on a null literal.
	if len(clean.InjectionDetail) != 0 {
		t.Errorf("InjectionDetail = %s, want empty for a NULL column", clean.InjectionDetail)
	}
	body, err := json.Marshal(clean)
	if err != nil {
		t.Fatalf("marshal FileRow: %v", err)
	}
	if json.Valid(body) && containsKey(body, "injectionDetail") {
		t.Errorf("a NULL detail must be omitted from the JSON, got %s", body)
	}
	if !containsKey(body, "injectionFlag") {
		t.Errorf("injectionFlag must always be present, got %s", body)
	}
}

// containsKey reports whether the marshalled object has the given top-level
// key, so the omitempty behaviour is asserted on the wire shape rather than
// on the Go zero value.
func containsKey(body []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
