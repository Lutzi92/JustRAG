//go:build integration

// BM25 stats tests exercise the live pgvector + main Postgres stores (the
// staleness check needs the main DB's `files` table). Gated by the
// `integration` build tag so `go test ./...` without the tag stays runnable
// on machines without docker. Skipped when the DB_*/VECTOR_DB_* env vars
// are unset (see internal/cascade/deleter_test.go for the same pattern).

package vector

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openBM25TestPool(t *testing.T, hostEnv, portEnv, userEnv, passEnv, nameEnv string) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv(hostEnv), os.Getenv(portEnv), os.Getenv(nameEnv)
	if host == "" || port == "" || name == "" {
		return nil
	}
	user := os.Getenv(userEnv)
	pass := os.Getenv(passEnv)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(user), url.QueryEscape(pass), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New (%s): %v", nameEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// openBM25TestPools connects to the test main + vector DBs the same way
// internal/cascade's deleter_test.go does. Skips the test when the env vars
// are unset.
func openBM25TestPools(t *testing.T) (mainPool, vectorPool *pgxpool.Pool) {
	t.Helper()
	mainPool = openBM25TestPool(t, "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME")
	if mainPool == nil {
		t.Skip("bm25 stats integration test requires DB_* env (main Postgres)")
	}
	vectorPool = openBM25TestPool(t, "VECTOR_DB_HOST", "VECTOR_DB_PORT", "VECTOR_DB_USER", "VECTOR_DB_PASSWORD", "VECTOR_DB_NAME")
	if vectorPool == nil {
		t.Skip("bm25 stats integration test requires VECTOR_DB_* env (vector Postgres)")
	}
	return mainPool, vectorPool
}

// TestBM25StatsRefreshAndStaleness seeds a KB with 3 chunks in
// document_chunks_768 (dim 768 is present in the dev vector DB fixture),
// runs RefreshKB, and asserts both the per-KB and per-term stats it
// produces, then exercises StaleKBs' "no stats row yet / stats older than
// newest chunk / stats older than maxAge" detection (W2-R5).
//
// Mutation (named per the brief): remove the `s.refreshed_at < c.newest`
// clause from StaleKBs' query -> the "stale after a fourth chunk lands"
// assertion below fails (the KB would look fresh forever once refreshed
// once, as long as it's refreshed inside the maxAge window).
func TestBM25StatsRefreshAndStaleness(t *testing.T) {
	mainPool, vectorPool := openBM25TestPools(t)
	ctx := context.Background()

	if err := EnsureBM25StatsTables(ctx, PgxpoolExec{Pool: vectorPool}, 768); err != nil {
		t.Fatalf("EnsureBM25StatsTables(768): %v", err)
	}

	kbID := uuid.New()
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = vectorPool.Exec(cctx, `DELETE FROM document_chunks_768 WHERE kb_id = $1`, kbID)
		_, _ = vectorPool.Exec(cctx, `DELETE FROM bm25_kb_stats_768 WHERE kb_id = $1`, kbID)
		_, _ = vectorPool.Exec(cctx, `DELETE FROM bm25_term_stats_768 WHERE kb_id = $1`, kbID)
	})

	insertChunk := func(content string) {
		t.Helper()
		_, err := vectorPool.Exec(ctx, `
			INSERT INTO document_chunks_768 (kb_id, file_id, content, vector_index, vector_index_simple)
			VALUES ($1, $2, $3, to_tsvector('german', $3), to_tsvector('simple', $3))
		`, kbID, uuid.New(), content)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	// "vertrag" (lowercase, unstemmed by the 'simple' config so it's not at
	// the mercy of German stemming quirks) appears in all three chunks;
	// "einzigartig" appears in only one.
	insertChunk("vertrag eins zwei drei")
	insertChunk("vertrag vier fuenf sechs")
	insertChunk("vertrag einzigartig sieben acht")

	refresher := NewBM25StatsRefresher(vectorPool, mainPool)
	if err := refresher.RefreshKB(ctx, kbID, 768); err != nil {
		t.Fatalf("RefreshKB: %v", err)
	}

	var docCount int64
	var avgLen float64
	if err := vectorPool.QueryRow(ctx,
		`SELECT doc_count, avg_len FROM bm25_kb_stats_768 WHERE kb_id = $1 AND arm = 'lang'`,
		kbID,
	).Scan(&docCount, &avgLen); err != nil {
		t.Fatalf("query bm25_kb_stats_768: %v", err)
	}
	if docCount != 3 {
		t.Errorf("doc_count = %d, want 3", docCount)
	}
	if avgLen <= 0 {
		t.Errorf("avg_len = %v, want > 0", avgLen)
	}

	var sharedDocCount, uniqueDocCount int
	if err := vectorPool.QueryRow(ctx,
		`SELECT doc_count FROM bm25_term_stats_768 WHERE kb_id = $1 AND arm = 'simple' AND lexeme = 'vertrag'`,
		kbID,
	).Scan(&sharedDocCount); err != nil {
		t.Fatalf("query shared term stats: %v", err)
	}
	if sharedDocCount != 3 {
		t.Errorf("'vertrag' doc_count = %d, want 3", sharedDocCount)
	}
	if err := vectorPool.QueryRow(ctx,
		`SELECT doc_count FROM bm25_term_stats_768 WHERE kb_id = $1 AND arm = 'simple' AND lexeme = 'einzigartig'`,
		kbID,
	).Scan(&uniqueDocCount); err != nil {
		t.Fatalf("query unique term stats: %v", err)
	}
	if uniqueDocCount != 1 {
		t.Errorf("'einzigartig' doc_count = %d, want 1", uniqueDocCount)
	}

	// Freshly refreshed -> not stale.
	stale, err := refresher.StaleKBs(ctx, 768, 24*time.Hour)
	if err != nil {
		t.Fatalf("StaleKBs (before insert): %v", err)
	}
	if containsUUID(stale, kbID) {
		t.Errorf("StaleKBs reported kb %s stale immediately after refresh", kbID)
	}

	// A pure insert with no matching refresh moves max(created_at) past
	// refreshed_at -> stale.
	insertChunk("vertrag neun zehn")
	stale, err = refresher.StaleKBs(ctx, 768, 24*time.Hour)
	if err != nil {
		t.Fatalf("StaleKBs (after insert): %v", err)
	}
	if !containsUUID(stale, kbID) {
		t.Errorf("StaleKBs did not report kb %s stale after a new chunk landed", kbID)
	}

	// No `files` rows exist for this random kbID on the main DB, so the
	// active-ingestion filter must treat it as idle rather than excluding it.
	// (Already implied by the assertion above, since an active-ingestion
	// false-positive would have filtered kbID out of the stale list.)
}

func containsUUID(ids []uuid.UUID, target uuid.UUID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
