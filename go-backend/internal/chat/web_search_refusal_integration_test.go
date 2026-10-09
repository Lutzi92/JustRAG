//go:build integration

// Integration test for the per-turn web-search refusal: a 422 must leave no
// chats row and no usage_events row in the real database. Skipped when DB_*
// env is unset; pool/skip pattern from store_pg_conflicts_integration_test.go.

package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/usage"
)

// seedUserAndKB creates a throwaway user + KB and removes them — plus every
// chat and usage_events row that points at the KB — on cleanup.
func seedUserAndKB(t *testing.T, pool *pgxpool.Pool, suffix string) (userID, kbID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`,
		"web-search-refusal-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		"web-search-refusal-kb-"+suffix, userID).Scan(&kbID); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM usage_events WHERE kb_id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM chats WHERE kb_id = $1::uuid`, kbID)        //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)         //nolint:errcheck
	})
	return userID, kbID
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql, kbID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, kbID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestSendMessage_WebSearch422WritesNoChatAndNoUsageRow sends a webSearch
// turn the server cannot honour (admin gate off) through the real handler
// over the real PGStore and the real usage recorder, then the same turn
// without webSearch as the positive control.
//
// Oracle: row counts read back from Postgres with plain SQL — independent of
// the handler. The control proves the same wiring DOES write one chats row
// and one usage_events row for an accepted turn; without it, "0 rows after
// the 422" could just mean the harness never writes anything. The usage
// write is asynchronous (PGRecorder.Record), so the control polls for it;
// the 422 request is sent first, so a stray write from it would have had at
// least the control's whole run to land before the final count.
func TestSendMessage_WebSearch422WritesNoChatAndNoUsageRow(t *testing.T) {
	pool := conflictTestPool(t)
	userID, kbID := seedUserAndKB(t, pool, time.Now().Format("150405.000000"))

	const chatsSQL = `SELECT count(*) FROM chats WHERE kb_id = $1::uuid`
	const usageSQL = `SELECT count(*) FROM usage_events WHERE kb_id = $1::uuid`

	h := &Handler{
		store:            NewStore(pool),
		searchService:    erroringSearcher{},
		usageRecorder:    usage.NewRecorder(pool),
		siteConfigReader: &fakeSiteConfigReader{values: webSearchConfig(false, false, "")}, // admin gate off
		toolDispatcher:   NewMCPDispatcher(newWebSearchFixtureRegistry(&invocationCounter{calls: map[string]int{}})),
	}
	send := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/kb/"+kbID+"/chat?stream=true", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = injectUser(r, userID)
		r.SetPathValue("id", kbID)
		w := httptest.NewRecorder()
		h.SendMessage(w, r)
		return w.Code
	}

	if code := send(`{"message":"hello","webSearch":true}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("webSearch with the admin gate off: status %d, want 422", code)
	}
	if n := countRows(t, pool, chatsSQL, kbID); n != 0 {
		t.Fatalf("chats rows after the 422 = %d, want 0", n)
	}

	// Positive control: the same request without webSearch is accepted
	// (chat + usage written) and then fails at the stub searcher.
	if code := send(`{"message":"hello"}`); code != http.StatusInternalServerError {
		t.Fatalf("control: status %d, want 500 from the stub searcher", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for countRows(t, pool, usageSQL, kbID) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := countRows(t, pool, chatsSQL, kbID); n != 1 {
		t.Fatalf("chats rows = %d, want exactly 1 (the control's)", n)
	}
	if n := countRows(t, pool, usageSQL, kbID); n != 1 {
		t.Fatalf("usage_events rows = %d, want exactly 1 (the control's)", n)
	}
}
