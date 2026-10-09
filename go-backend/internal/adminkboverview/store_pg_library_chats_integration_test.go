//go:build integration

package adminkboverview_test

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/adminkboverview"
)

// TestChatStatsByKB_IgnoresLibraryChats pins phase-3 P3-R1: a KB-less library
// chat with messages must neither error the aggregate (NULL kb_id scanned into
// a string) nor appear as a bucket. Mutation: dropping the `kb_id IS NOT NULL`
// filter makes the scan fail.
func TestChatStatsByKB_IgnoresLibraryChats(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	var userID, kbID, kbChat, libChat string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ('kboverview-libchat', 'x-not-a-real-hash', 'user') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('kboverview-libchat', 'public') RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM chats WHERE user_id = $1::uuid`, userID)    //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)         //nolint:errcheck
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, 'kb') RETURNING id::text`, kbID, userID).Scan(&kbChat); err != nil {
		t.Fatalf("seed kb chat: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title, type) VALUES (NULL, $1::uuid, 'lib', 'library') RETURNING id::text`, userID).Scan(&libChat); err != nil {
		t.Fatalf("seed library chat: %v", err)
	}
	for _, c := range []string{kbChat, libChat} {
		if _, err := pool.Exec(ctx, `INSERT INTO messages (chat_id, role, content) VALUES ($1::uuid, 'user', 'hi')`, c); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	stats, err := adminkboverview.NewStore(pool).ChatStatsByKB(ctx)
	if err != nil {
		t.Fatalf("ChatStatsByKB: %v", err)
	}
	if got := stats[kbID].ChatCount; got != 1 {
		t.Fatalf("KB chat count = %d, want 1", got)
	}
	if _, ok := stats[""]; ok {
		t.Fatal("library chats must not form a bucket")
	}
}
