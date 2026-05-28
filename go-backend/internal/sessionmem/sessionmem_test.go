package sessionmem

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInMemoryStore_AppendNoteAndGet(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	if err := s.AppendNote(ctx, "chat1", SessionNote{Text: "user prefers concise answers", Source: "agent"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.AppendNote(ctx, "chat1", SessionNote{Text: "scope: only docs from 2024", Source: "user_explicit"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	mem, err := s.Get(ctx, "chat1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := len(mem.Notes); got != 2 {
		t.Errorf("notes = %d, want 2", got)
	}
	if mem.Notes[0].Text != "user prefers concise answers" {
		t.Errorf("first note text mismatch: %q", mem.Notes[0].Text)
	}
}

func TestInMemoryStore_NoteTooLong(t *testing.T) {
	s := NewInMemoryStore()
	long := strings.Repeat("x", MaxNoteChars+1)
	err := s.AppendNote(context.Background(), "chat1", SessionNote{Text: long})
	if !errors.Is(err, ErrNoteTooLong) {
		t.Errorf("expected ErrNoteTooLong, got %v", err)
	}
}

func TestInMemoryStore_RingBufferFIFO(t *testing.T) {
	s := NewInMemoryStore()
	for i := 0; i < MaxNotes+10; i++ {
		_ = s.AppendNote(context.Background(), "chat1", SessionNote{Text: "n" + itoa(i)})
	}
	mem, _ := s.Get(context.Background(), "chat1")
	if got := len(mem.Notes); got != MaxNotes {
		t.Errorf("len after overflow = %d, want %d (FIFO ring)", got, MaxNotes)
	}
	// First retained note should be the (MaxNotes+10 - MaxNotes)th = 10th.
	if mem.Notes[0].Text != "n10" {
		t.Errorf("oldest retained note = %q, want n10", mem.Notes[0].Text)
	}
}

func TestWriteCounter_RateLimit(t *testing.T) {
	c := NewWriteCounter()
	for i := 0; i < MaxWritesPerTurn; i++ {
		if !c.Inc() {
			t.Fatalf("write %d should be allowed", i+1)
		}
	}
	// 6th call must fail closed.
	if c.Inc() {
		t.Errorf("write %d (over cap) was allowed; rate limit not enforced", MaxWritesPerTurn+1)
	}
	// The atomic counter increments unconditionally; over-cap increments
	// are harmless because the counter is turn-scoped. The gate is
	// Inc()'s return value, not the recorded count.
	if got := c.Count(); got < MaxWritesPerTurn {
		t.Errorf("Count after over-cap = %d, want >= %d", got, MaxWritesPerTurn)
	}
}

func TestFormatPrompt_EmptyReturnsEmptyString(t *testing.T) {
	if got := FormatPrompt(SessionMemory{}, "en", 10); got != "" {
		t.Errorf("empty memory should produce empty prompt, got %q", got)
	}
}

func TestFormatPrompt_LimitsAndLanguage(t *testing.T) {
	m := SessionMemory{Notes: []SessionNote{
		{Text: "old"},
		{Text: "newer"},
		{Text: "newest"},
	}}
	got := FormatPrompt(m, "de", 2)
	if !strings.Contains(got, "Konversations-Gedächtnis") {
		t.Errorf("German prompt should use German header, got %q", got)
	}
	if !strings.Contains(got, "newest") || !strings.Contains(got, "newer") {
		t.Errorf("most-recent notes should be retained, got %q", got)
	}
	if strings.Contains(got, "old") {
		t.Errorf("limit=2 should drop the oldest note, got %q", got)
	}
}

// TestRedisStore_ConcurrentAppendNoteNoLostWrites guards the per-chatID
// striped-mutex serialization in RedisStore. Without the lock, the
// GET-modify-SET path drops writes when goroutines interleave; with the
// lock every appended note must survive (up to the FIFO ring cap).
func TestRedisStore_ConcurrentAppendNoteNoLostWrites(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := NewRedisStore(client, 0)
	ctx := context.Background()

	// Stay below the MaxNotes FIFO cap so every write is observable.
	const n = MaxNotes
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if err := store.AppendNote(ctx, "chat-shared", SessionNote{Text: "n" + itoa(i)}); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	mem, err := store.Get(ctx, "chat-shared")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := len(mem.Notes); got != n {
		t.Fatalf("notes after %d concurrent appends = %d, want %d (writes were lost)", n, got, n)
	}
}

// itoa is a tiny stdlib-free integer formatter so the test stays self-
// contained (sessionmem imports strconv via fmt; using strconv here would
// pull in another import for one test helper).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if negative {
		buf = append(buf, '-')
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
