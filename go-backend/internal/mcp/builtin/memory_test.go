package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/sessionmem"
)

func TestMemoryWrite_RateLimitedAfterFiveCalls(t *testing.T) {
	store := sessionmem.NewInMemoryStore()
	counter := sessionmem.NewWriteCounter()
	tool := NewMemoryWrite(store, counter)

	for i := 0; i < sessionmem.MaxWritesPerTurn; i++ {
		args, _ := json.Marshal(map[string]string{"chat_id": "c1", "text": "note " + itoa(i)})
		if _, err := tool.Handler.Invoke(context.Background(), args); err != nil {
			t.Fatalf("write %d should succeed: %v", i+1, err)
		}
	}
	args, _ := json.Marshal(map[string]string{"chat_id": "c1", "text": "one too many"})
	_, err := tool.Handler.Invoke(context.Background(), args)
	if !errors.Is(err, sessionmem.ErrWriteRateLimited) {
		t.Errorf("write %d (over cap) should return ErrWriteRateLimited, got %v", sessionmem.MaxWritesPerTurn+1, err)
	}
	mem, _ := store.Get(context.Background(), "c1")
	if got := len(mem.Notes); got != sessionmem.MaxWritesPerTurn {
		t.Errorf("persisted note count = %d, want %d (rate-limited write must NOT reach the store)", got, sessionmem.MaxWritesPerTurn)
	}
}

func TestMemoryWrite_RejectsMissingChatID(t *testing.T) {
	tool := NewMemoryWrite(sessionmem.NewInMemoryStore(), nil)
	args, _ := json.Marshal(map[string]string{"text": "note without chat_id"})
	if _, err := tool.Handler.Invoke(context.Background(), args); err == nil {
		t.Error("expected error when chat_id is missing")
	}
}

// TestMemoryWrite_HonorsContextCounter proves that the memory_write
// tool reads the per-turn counter from the context (sessionmem.WithWriteCounter)
// when no explicit override is supplied. Two simulated "turns" using
// different counters must not interfere — the second turn starts with
// a fresh budget.
func TestMemoryWrite_HonorsContextCounter(t *testing.T) {
	store := sessionmem.NewInMemoryStore()
	tool := NewMemoryWrite(store, nil) // no override; read from context

	// Turn 1: exhaust the counter.
	turn1 := sessionmem.NewWriteCounter()
	ctx1 := sessionmem.WithWriteCounter(context.Background(), turn1)
	for i := 0; i < sessionmem.MaxWritesPerTurn; i++ {
		args, _ := json.Marshal(map[string]string{"chat_id": "c1", "text": "t1-" + itoa(i)})
		if _, err := tool.Handler.Invoke(ctx1, args); err != nil {
			t.Fatalf("turn 1 write %d should succeed: %v", i+1, err)
		}
	}
	args, _ := json.Marshal(map[string]string{"chat_id": "c1", "text": "t1-over"})
	if _, err := tool.Handler.Invoke(ctx1, args); !errors.Is(err, sessionmem.ErrWriteRateLimited) {
		t.Errorf("turn 1: 6th write must be rate limited, got %v", err)
	}

	// Turn 2: fresh counter — first write allowed again, proving the
	// counter is per-turn (not global / not chat-scoped).
	turn2 := sessionmem.NewWriteCounter()
	ctx2 := sessionmem.WithWriteCounter(context.Background(), turn2)
	args, _ = json.Marshal(map[string]string{"chat_id": "c1", "text": "t2-fresh"})
	if _, err := tool.Handler.Invoke(ctx2, args); err != nil {
		t.Errorf("turn 2 first write should succeed under a fresh counter: %v", err)
	}
}

// TestMemoryWrite_NoCounterMeansNoRateLimit confirms the documented
// fallback: when the context has no counter and no override is wired,
// the tool runs unbounded (intentional — production attaches a
// counter; tests + eval harness don't need one).
func TestMemoryWrite_NoCounterMeansNoRateLimit(t *testing.T) {
	store := sessionmem.NewInMemoryStore()
	tool := NewMemoryWrite(store, nil)
	for i := 0; i < sessionmem.MaxWritesPerTurn*3; i++ {
		args, _ := json.Marshal(map[string]string{"chat_id": "c1", "text": "n" + itoa(i)})
		if _, err := tool.Handler.Invoke(context.Background(), args); err != nil {
			t.Fatalf("write %d should succeed without a counter; got %v", i+1, err)
		}
	}
}

func TestMemoryRead_ReturnsFormattedPrompt(t *testing.T) {
	store := sessionmem.NewInMemoryStore()
	_ = store.AppendNote(context.Background(), "c1", sessionmem.SessionNote{Text: "scope: only 2024 docs", Source: "agent"})

	tool := NewMemoryRead(store)
	args, _ := json.Marshal(map[string]string{"chat_id": "c1"})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(res.Text, "scope: only 2024 docs") {
		t.Errorf("formatted prompt should include the note text, got %q", res.Text)
	}
	// Structured payload should round-trip into a SessionMemory.
	var rt sessionmem.SessionMemory
	if err := json.Unmarshal(res.Structured, &rt); err != nil {
		t.Fatalf("structured payload not parseable: %v", err)
	}
	if len(rt.Notes) != 1 {
		t.Errorf("structured Notes len = %d, want 1", len(rt.Notes))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
