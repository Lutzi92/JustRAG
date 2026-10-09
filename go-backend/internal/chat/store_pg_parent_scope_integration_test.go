//go:build integration

// Integration tests for the cross-chat history guard: neither the ancestor
// walk nor a stored parent link may leave a chat. Run through withdb.sh.

package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPGStore_AncestorWalkNeverLeavesTheChat(t *testing.T) {
	pool := conflictTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	chatA := seedChat(t, pool, "scope-a")
	chatB := seedChat(t, pool, "scope-b")

	secret, err := store.AddMessage(ctx, AddMessageParams{ChatID: chatB, Role: "user", Content: "GEHEIMNIS"})
	if err != nil {
		t.Fatal(err)
	}

	// A legacy row already linking across chats (written before the guard):
	// the walk from it must stop at the chat boundary.
	var bridge string
	if err := pool.QueryRow(ctx, `
		INSERT INTO messages (chat_id, role, content, parent_message_id)
		VALUES ($1::uuid, 'user', 'bridge', $2::uuid) RETURNING id::text`, chatA, secret.ID).Scan(&bridge); err != nil {
		t.Fatal(err)
	}
	rows, err := store.GetMessageAncestors(ctx, bridge, chatA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != bridge {
		t.Fatalf("ancestors = %+v, want only the bridge row", rows)
	}
	for _, r := range rows {
		if r.Content == "GEHEIMNIS" {
			t.Fatal("ancestor walk crossed into another chat")
		}
	}

	// A legitimate same-chat chain still walks completely.
	q, _ := store.AddMessage(ctx, AddMessageParams{ChatID: chatA, Role: "user", Content: "q1"})
	a, _ := store.AddMessage(ctx, AddMessageParams{ChatID: chatA, Role: "ai", Content: "a1", ParentMessageID: &q.ID})
	q2, _ := store.AddMessage(ctx, AddMessageParams{ChatID: chatA, Role: "user", Content: "q2", ParentMessageID: &a.ID})
	chain, err := store.GetMessageAncestors(ctx, q2.ID, chatA)
	if err != nil || len(chain) != 3 {
		t.Fatalf("same-chat chain = %d rows, %v", len(chain), err)
	}
	if q2.ParentMessageID == nil || *q2.ParentMessageID != a.ID {
		t.Fatalf("same-chat parent not stored: %v", q2.ParentMessageID)
	}
}

func TestPGStore_AddMessageDropsForeignParent(t *testing.T) {
	pool := conflictTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	chatA := seedChat(t, pool, "drop-a")
	chatB := seedChat(t, pool, "drop-b")
	secret, err := store.AddMessage(ctx, AddMessageParams{ChatID: chatB, Role: "user", Content: "GEHEIMNIS"})
	if err != nil {
		t.Fatal(err)
	}
	missing := uuid.NewString()
	for _, parent := range []string{secret.ID, missing} {
		msg, err := store.AddMessage(ctx, AddMessageParams{ChatID: chatA, Role: "user", Content: "hi", ParentMessageID: &parent})
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if msg.ParentMessageID != nil {
			t.Errorf("parent %s stored as %q, want NULL", parent, *msg.ParentMessageID)
		}
	}

	in, err := store.MessageInChat(ctx, secret.ID, chatB)
	if err != nil || !in {
		t.Errorf("MessageInChat own = %v, %v", in, err)
	}
	if in, err := store.MessageInChat(ctx, secret.ID, chatA); err != nil || in {
		t.Errorf("MessageInChat foreign = %v, %v", in, err)
	}
	if in, err := store.MessageInChat(ctx, "not-a-uuid", chatA); err != nil || in {
		t.Errorf("MessageInChat malformed = %v, %v", in, err)
	}
}

func TestPGStore_ReplaceChatFileRefsMissingFile(t *testing.T) {
	pool := conflictTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	userID := seedLibraryUser(t, pool, "refs-gone")
	chat, err := store.CreateLibraryChat(ctx, userID, "x")
	if err != nil {
		t.Fatal(err)
	}
	err = store.ReplaceChatFileRefs(ctx, chat.ID, []string{uuid.NewString()})
	if !errors.Is(err, ErrChatFileRefGone) {
		t.Fatalf("err = %v, want ErrChatFileRefGone", err)
	}
}
