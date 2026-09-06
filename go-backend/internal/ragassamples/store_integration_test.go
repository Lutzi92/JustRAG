//go:build integration

// Integration tests for PGStore against a live main Postgres. Skipped when
// DB_* env is unset. Pool/skip pattern follows
// internal/usage/store_pg_integration_test.go.

package ragassamples_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ragassamples"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("ragassamples integration tests require DB_* env (main Postgres)")
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

// insertKB creates a throwaway user + KB and returns both ids. The cleanup
// removes this test's ragas_samples rows first — the FK is ON DELETE SET NULL,
// so dropping the KB alone would leave orphan rows behind in a shared dev DB.
func insertKB(t *testing.T, pool *pgxpool.Pool, suffix string) (userID, kbID string) {
	t.Helper()
	ctx := context.Background()
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`,
		"ragassamples-test-"+suffix).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		"ragassamples-kb-"+suffix, userID).Scan(&kbID)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ragas_samples WHERE kb_id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID)  //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)          //nolint:errcheck
	})
	return userID, kbID
}

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func countRows(t *testing.T, pool *pgxpool.Pool, kbID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM ragas_samples WHERE kb_id = $1::uuid`, kbID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestInsert_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, kbID := insertKB(t, pool, "roundtrip")
	store := ragassamples.NewStore(pool)

	s := ragassamples.Sample{
		KbID:             str(kbID),
		Faithfulness:     f64(0.75),
		AnswerRelevance:  f64(0.5),
		ContextPrecision: nil, // judge failed this prompt
		Coverage:         nil,
		JudgeModel:       "gemma-4-26b",
		JudgeErrors:      []string{"context_precision: timeout"},
		SampledAt:        time.Now().Add(-2 * time.Hour),
	}
	if err := store.Insert(ctx, s); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var (
		faith, ar, cp, cov *float64
		model              string
		errs               []string
		sampledAt          time.Time
		messageID          *string
	)
	err := pool.QueryRow(ctx, `
		SELECT faithfulness, answer_relevance, context_precision, coverage,
		       judge_model, COALESCE(judge_errors, '[]'::jsonb), sampled_at, message_id::text
		  FROM ragas_samples WHERE kb_id = $1::uuid`, kbID).
		Scan(&faith, &ar, &cp, &cov, &model, &errs, &sampledAt, &messageID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if faith == nil || *faith < 0.749 || *faith > 0.751 {
		t.Errorf("faithfulness = %v, want ~0.75", faith)
	}
	if cp != nil || cov != nil {
		t.Errorf("nil scores must stay NULL, got cp=%v cov=%v", cp, cov)
	}
	if model != "gemma-4-26b" {
		t.Errorf("judge_model = %q", model)
	}
	if len(errs) != 1 || errs[0] != "context_precision: timeout" {
		t.Errorf("judge_errors = %v", errs)
	}
	if messageID != nil {
		t.Errorf("message_id = %v, want NULL for an empty MessageID", *messageID)
	}
	if d := time.Since(sampledAt); d < 90*time.Minute || d > 150*time.Minute {
		t.Errorf("sampled_at is %v old, want ~2h (the caller's timestamp, not now())", d)
	}
}

// A zero SampledAt must land as now() rather than year 1 — a row stamped 0001
// would be pruned by the very next retention pass.
func TestInsert_ZeroSampledAtDefaultsToNow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, kbID := insertKB(t, pool, "zerots")
	store := ragassamples.NewStore(pool)

	if err := store.Insert(ctx, ragassamples.Sample{KbID: str(kbID), Faithfulness: f64(1)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	var sampledAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT sampled_at FROM ragas_samples WHERE kb_id = $1::uuid`, kbID).Scan(&sampledAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if d := time.Since(sampledAt); d > time.Minute || d < -time.Minute {
		t.Errorf("sampled_at is %v from now, want ~0", d)
	}
}

func TestDailyStats_AveragesNonNilOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, kbID := insertKB(t, pool, "daily")
	store := ragassamples.NewStore(pool)

	now := time.Now()
	rows := []ragassamples.Sample{
		{KbID: str(kbID), Faithfulness: f64(1.0), AnswerRelevance: f64(0.5), SampledAt: now.Add(-1 * time.Hour)},
		{KbID: str(kbID), Faithfulness: f64(0.0), AnswerRelevance: nil, SampledAt: now.Add(-2 * time.Hour)},
		// Outside the window — must not move the mean or the count.
		{KbID: str(kbID), Faithfulness: f64(0.2), SampledAt: now.Add(-48 * time.Hour)},
	}
	for _, r := range rows {
		if err := store.Insert(ctx, r); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	stats, err := store.DailyStats(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DailyStats: %v", err)
	}
	got, ok := stats[kbID]
	if !ok {
		t.Fatalf("no stats for kb %s (keys: %v)", kbID, len(stats))
	}
	if got.N != 2 {
		t.Errorf("N = %d, want 2 (the out-of-window row must be excluded)", got.N)
	}
	if got.Faithfulness == nil || *got.Faithfulness != 0.5 {
		t.Errorf("faithfulness mean = %v, want 0.5", got.Faithfulness)
	}
	// Only ONE of the two in-window rows carries an answer_relevance: the
	// mean must be 0.5 (that row alone), not 0.25 (a NULL counted as zero).
	if got.AnswerRelevance == nil || *got.AnswerRelevance != 0.5 {
		t.Errorf("answer_relevance mean = %v, want 0.5 (NULLs excluded from the mean)", got.AnswerRelevance)
	}
	if got.ContextPrecision != nil {
		t.Errorf("context_precision mean = %v, want nil (no row carried one)", *got.ContextPrecision)
	}
}

// Rows whose KB has been deleted (kb_id SET NULL) still count toward the
// table's size but must not mint a gauge series under an empty KB label.
func TestDailyStats_SkipsNullKB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := ragassamples.NewStore(pool)

	// This row has no KB, so the per-KB cleanup in insertKB cannot reach it and
	// it has to clean up after itself — by PRIMARY KEY. A predicate over the
	// score column would not do: faithfulness is `real`, so a `= 0.3` literal
	// is a float8 that the widened float4 never equals, and the DELETE would
	// silently match nothing, leaking one row into the shared dev DB per run.
	// The marker is only how the id is located, never what is deleted.
	const marker = "ragassamples-test-nullkb"
	if err := store.Insert(ctx, ragassamples.Sample{Faithfulness: f64(0.3), JudgeModel: marker}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	var id string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM ragas_samples WHERE judge_model = $1`, marker).Scan(&id); err != nil {
		t.Fatalf("locate the inserted row: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ragas_samples WHERE id = $1::uuid`, id) //nolint:errcheck
	})

	stats, err := store.DailyStats(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DailyStats: %v", err)
	}
	if _, ok := stats[""]; ok {
		t.Error("a NULL kb_id produced an empty-string map key")
	}
}

// The prune window is deliberately set in the 1990s so the shared dev DB
// cannot lose real rows: nothing in production predates 1995.
func TestPrune_DeletesOnlyOlderRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, kbID := insertKB(t, pool, "prune")
	store := ragassamples.NewStore(pool)

	old := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	young := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, ts := range []time.Time{old, young} {
		if err := store.Insert(ctx, ragassamples.Sample{KbID: str(kbID), Faithfulness: f64(1), SampledAt: ts}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if n := countRows(t, pool, kbID); n != 2 {
		t.Fatalf("seeded rows = %d, want 2", n)
	}

	deleted, err := store.Prune(ctx, time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted < 1 {
		t.Errorf("deleted = %d, want at least the one 1990 row", deleted)
	}
	if n := countRows(t, pool, kbID); n != 1 {
		t.Errorf("rows left = %d, want 1 (the 2000 row must survive)", n)
	}
}
