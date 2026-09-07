//go:build integration

// Integration test for the W5-R7 `messages.conflicts` column: the report is
// known at answer time, so it must survive AddMessage → GetChatMessages
// without a second UPDATE. Skipped when DB_* env is unset. Pool/skip pattern
// follows internal/ragassamples/store_integration_test.go.

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func conflictTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("chat conflicts integration test requires DB_* env (main Postgres)")
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

// seedChat creates a throwaway user + KB + chat and cleans all three up by
// id (the messages cascade off the chat).
func seedChat(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	ctx := context.Background()
	var userID, kbID, chatID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`,
		"chat-conflicts-test-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		"chat-conflicts-kb-"+suffix, userID).Scan(&kbID); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, $3) RETURNING id::text`,
		kbID, userID, "conflicts-"+suffix).Scan(&chatID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM chats WHERE id = $1::uuid`, chatID)         //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)         //nolint:errcheck
	})
	return chatID
}

func TestPGStore_ConflictsRoundTrip(t *testing.T) {
	pool := conflictTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	chatID := seedChat(t, pool, "roundtrip")

	report := []MessageConflict{{
		Claim:   "Beitragshöhe",
		SourceA: 1,
		SourceB: 3,
		Kind:    "superseded",
		Newer:   "b",
		FileA:   "alt.md",
		FileB:   "neu.md",
	}}

	msg, err := store.AddMessage(ctx, AddMessageParams{
		ChatID:    chatID,
		Role:      "ai",
		Content:   "answer",
		Conflicts: report,
	})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	// The INSERT ... RETURNING path decodes the column too.
	if len(msg.Conflicts) != 1 {
		t.Fatalf("AddMessage returned Conflicts = %+v, want one entry", msg.Conflicts)
	}

	rows, err := store.GetChatMessages(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatMessages: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("messages: got %d, want 1", len(rows))
	}
	got := rows[0].Conflicts
	if len(got) != 1 {
		t.Fatalf("Conflicts = %+v, want one entry", got)
	}
	if got[0] != report[0] {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got[0], report[0])
	}

	// ONE wire shape: what GET .../messages serialises must be the bare
	// array, so the frontend reads message.conflicts[0].claim — identical to
	// the live SSE frame.
	blob, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal message row: %v", err)
	}
	var wire struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	if err := json.Unmarshal(blob, &wire); err != nil {
		t.Fatalf("conflicts is not a bare array on the wire: %v\n%s", err, blob)
	}
	if len(wire.Conflicts) != 1 || wire.Conflicts[0]["claim"] != "Beitragshöhe" {
		t.Fatalf("message.conflicts[0].claim is not readable: %s", blob)
	}

	// The ancestor walk reads the same column list.
	anc, err := store.GetMessageAncestors(ctx, msg.ID, chatID)
	if err != nil {
		t.Fatalf("GetMessageAncestors: %v", err)
	}
	if len(anc) != 1 || len(anc[0].Conflicts) != 1 {
		t.Fatalf("GetMessageAncestors dropped conflicts: %+v", anc)
	}
}

// A turn with no conflicts must write SQL NULL and read back as nil, so an
// existing client's `conflicts` handling stays "absent means not run".
func TestPGStore_ConflictsNilIsNull(t *testing.T) {
	pool := conflictTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	chatID := seedChat(t, pool, "null")

	msg, err := store.AddMessage(ctx, AddMessageParams{ChatID: chatID, Role: "ai", Content: "answer"})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if len(msg.Conflicts) != 0 {
		t.Errorf("Conflicts = %+v, want empty", msg.Conflicts)
	}
	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT conflicts IS NULL FROM messages WHERE id = $1::uuid`, msg.ID).Scan(&isNull); err != nil {
		t.Fatalf("probe column: %v", err)
	}
	if !isNull {
		t.Error("messages.conflicts is not SQL NULL for a turn with no conflicts")
	}
}
