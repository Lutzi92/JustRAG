//go:build integration

// Verifies the last_success_at asymmetry on rss_feeds (migration 0071):
// only a successful poll may move it. Requires a live main Postgres;
// skipped when DB_* env is unset.

package rss_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/rss"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("rss last_success_at tests require DB_* env (main Postgres)")
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

// Mutation: add `last_success_at = NOW()` to UpdateRSSFeedPollFailure (or
// drop it from the success twin) → this fails. A failing feed keeps
// refreshing last_polled_at, so a shared timestamp would make a permanently
// broken feed look freshly synced — the exact blindness this column removes.
func TestRSSPollSuccessAndFailure_LastSuccessAsymmetry(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := rss.NewStore(pool)

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('rss-last-success', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	var feedID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status) VALUES ($1::uuid, 'https://example.invalid/f.xml', 'active')
		RETURNING id::text`, kbID).Scan(&feedID); err != nil {
		t.Fatalf("seed feed: %v", err)
	}

	read := func() (polled *time.Time, success *time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT last_polled_at, last_success_at FROM rss_feeds WHERE id = $1::uuid`, feedID).
			Scan(&polled, &success); err != nil {
			t.Fatalf("read feed: %v", err)
		}
		return polled, success
	}

	if _, success := read(); success != nil {
		t.Fatalf("a fresh feed must start with a NULL last_success_at, got %v", success)
	}

	if err := store.UpdateRSSFeedPollSuccess(ctx, feedID, 7); err != nil {
		t.Fatalf("UpdateRSSFeedPollSuccess: %v", err)
	}
	_, afterSuccess := read()
	if afterSuccess == nil {
		t.Fatal("a successful poll must stamp last_success_at")
	}

	if err := store.UpdateRSSFeedPollFailure(ctx, feedID, "boom"); err != nil {
		t.Fatalf("UpdateRSSFeedPollFailure: %v", err)
	}
	polledAfterFailure, successAfterFailure := read()
	if successAfterFailure == nil || !successAfterFailure.Equal(*afterSuccess) {
		t.Errorf("a failed poll must leave last_success_at untouched: %v -> %v", afterSuccess, successAfterFailure)
	}
	if polledAfterFailure == nil || !polledAfterFailure.After(*afterSuccess) {
		t.Errorf("a failed poll must still move last_polled_at: %v", polledAfterFailure)
	}
}
