//go:build integration

// Verifies the sweeper store contract (Kind/ListDue/ListUnscheduled/
// MarkScheduled/SetSchedule) for GoldenSetStore and LatestCompletedScheduled
// on Store, against a live main Postgres. Skipped when DB_* env is unset;
// note the repo .env sets DB_HOST=db, so run with DB_HOST=localhost or these
// tests do not run at all.

package eval

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/syncwindow"
)

// integrationPool mirrors internal/syncsched/migration_integration_test.go's
// testPool: skip when DB_* env is unset (the repo .env sets DB_HOST=db,
// which only resolves inside compose).
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("eval schedule integration tests require DB_* env (main Postgres)")
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

// insertKB creates a throwaway KB and returns its id. eval_golden_sets.kb_id
// carries a NOT NULL FK to knowledge_bases (internal/migrate.EnsureGoldenSetKBID
// enforces it once no orphan rows remain), so Create needs a real row here —
// mirrors internal/syncsched/migration_integration_test.go's insertKB.
func insertKB(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	name := "eval-sched-" + t.Name() + "-" + uuid.NewString()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, id)
	})
	return id
}

func TestGoldenSetStore_ScheduleContract(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	s := NewGoldenSetStore(pool)

	id, _, err := s.Create(ctx, GoldenSet{Name: "sched-" + uuid.NewString(), Content: []byte(`[]`), ContentHash: "h", QuestionCount: 0, KBID: insertKB(t, pool)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.Delete(ctx, id) })

	if s.Kind() != "eval" {
		t.Fatalf("Kind: want eval, got %q", s.Kind())
	}
	// manual → neither due nor unscheduled
	if due, _ := s.ListDue(ctx, time.Now()); containsID(due, id) {
		t.Fatal("manual set must not be due")
	}
	if un, _ := s.ListUnscheduled(ctx); containsID(un, id) {
		t.Fatal("manual set must not be unscheduled")
	}
	// daily, unstamped → unscheduled only
	if ok, err := s.SetSchedule(ctx, id, "daily"); err != nil || !ok {
		t.Fatalf("SetSchedule: ok=%v err=%v", ok, err)
	}
	if un, _ := s.ListUnscheduled(ctx); !containsID(un, id) {
		t.Fatal("daily unstamped set must be unscheduled")
	}
	if due, _ := s.ListDue(ctx, time.Now()); containsID(due, id) {
		t.Fatal("unstamped set must not be due")
	}
	// stamped in the past → due
	if err := s.MarkScheduled(ctx, id.String(), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.ListDue(ctx, time.Now()); !containsID(due, id) {
		t.Fatal("past-stamped set must be due")
	}
	// back to manual resets the stamp
	if _, err := s.SetSchedule(ctx, id, "manual"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schedule != "manual" || got.NextRunAt != nil {
		t.Fatalf("manual must clear next_run_at: %+v", got)
	}
	// invalid value rejected
	if _, err := s.SetSchedule(ctx, id, "hourly"); err == nil {
		t.Fatal("invalid schedule must error")
	}
}

func containsID(rows []syncwindow.DueSource, id uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == id.String() {
			return true
		}
	}
	return false
}

func TestStore_LatestCompletedScheduled(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	rs := NewStore(pool)
	gs := NewGoldenSetStore(pool)
	kb := insertKB(t, pool)
	gsID, _, err := gs.Create(ctx, GoldenSet{Name: "lcs-" + uuid.NewString(), Content: []byte(`[]`), ContentHash: "h", KBID: kb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = gs.Delete(ctx, gsID) })

	mk := func(scheduled bool, report string) uuid.UUID {
		id, err := rs.Insert(ctx, Run{KBID: kb, GoldenSetID: &gsID, FixtureHash: "h", ConfigSnapshot: []byte(`{}`), TopK: 10, Scheduled: scheduled})
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.MarkRunning(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := rs.MarkCompleted(ctx, id, []byte(report)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	manual := mk(false, `{"aggregate":{"mean_recall":0.1}}`)
	first := mk(true, `{"aggregate":{"mean_recall":0.5}}`)
	second := mk(true, `{"aggregate":{"mean_recall":0.6}}`)
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{manual, first, second} {
			_, _, _ = rs.Delete(ctx, id)
		}
	})

	prev, err := rs.LatestCompletedScheduled(ctx, gsID, second)
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.ID != first {
		t.Fatalf("want previous scheduled run %s, got %+v", first, prev)
	}
	none, err := rs.LatestCompletedScheduled(ctx, gsID, first)
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Fatalf("first scheduled run has no predecessor (manual runs must be excluded), got %+v", none)
	}
}
