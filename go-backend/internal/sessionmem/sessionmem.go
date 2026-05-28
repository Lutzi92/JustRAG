// Package sessionmem implements the Phase 2 §2.3 chat-session memory:
// a small Redis-backed semantic scratchpad scoped to one chat ID. It
// holds short free-form notes and chunk references the agent decided
// to remember, so multi-turn conversations can carry findings forward
// without re-retrieval.
//
// Wire-shape: one JSON blob per chat at key `chat_session:<chat_id>`
// with a 30-day TTL. The store is intentionally tiny — tests can fake
// it with a map; production uses *RedisStore.
package sessionmem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultTTL is the per-session TTL applied to every write. A user who
// returns to a chat after 30 days of inactivity gets a fresh memory
// state — simpler than building expiry inside the JSON blob.
const DefaultTTL = 30 * 24 * time.Hour

// MaxNotes caps the per-session notes array length. New writes evict
// the oldest note once the cap is hit (FIFO). Keeping the array small
// means the orchestrator's turn-start `Conversation memory:` block
// stays short — feeding 200 notes into a chat prompt would defeat the
// purpose.
const MaxNotes = 50

// MaxNoteChars caps the byte length of each note. The plan recommends
// 280 — same as a tweet. This is a soft upper bound enforced at write;
// callers receive an error rather than silent truncation.
const MaxNoteChars = 280

// MaxWritesPerTurn is the rate-limit gate applied within one chat turn.
// Tracked outside this package — see WriteCounter — because the
// "what counts as one turn" boundary is the orchestrator's
// responsibility.
const MaxWritesPerTurn = 5

// ErrWriteRateLimited is returned from Memory.Write when the per-turn
// write counter has been exceeded. The orchestrator surfaces this as a
// `agent_notes_rate_limited` trajectory event and proceeds without
// persisting the note.
var ErrWriteRateLimited = errors.New("sessionmem: write rate limit exceeded for this turn")

// ErrNoteTooLong is returned when a note exceeds MaxNoteChars bytes.
var ErrNoteTooLong = errors.New("sessionmem: note exceeds max length")

// SessionMemory is the on-disk shape stored in Redis.
type SessionMemory struct {
	Notes     []SessionNote `json:"notes"`
	Findings  []FindingRef  `json:"findings,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SessionNote is one durable fact the agent or user added to the
// session. Source is "agent" when the LLM chose to memorize it,
// "user_explicit" when the user asked the system to remember it
// directly. The split lets the UI render or filter them differently.
type SessionNote struct {
	Text    string    `json:"text"`
	Source  string    `json:"source"`
	AddedAt time.Time `json:"added_at"`
}

// FindingRef is a chunk reference the agent decided to keep around
// across turns (e.g. "the team-list chunk for project Alpha"). The
// content itself is NOT stored — only the file_id + a deterministic
// chunk hash so re-retrieval can resolve it. Phase 2 §2.3 leaves the
// resolution path for the LLM-tool layer to implement; v1 just stores
// the metadata.
type FindingRef struct {
	FileID    string    `json:"file_id"`
	ChunkHash string    `json:"chunk_hash"`
	AddedAt   time.Time `json:"added_at"`
}

// Store is the persistence interface. *RedisStore is the production
// implementation; tests inject InMemoryStore.
type Store interface {
	Get(ctx context.Context, chatID string) (SessionMemory, error)
	AppendNote(ctx context.Context, chatID string, note SessionNote) error
	AppendFinding(ctx context.Context, chatID string, ref FindingRef) error
	Delete(ctx context.Context, chatID string) error
}

// ----- Redis-backed store ------------------------------------------------

// RedisStore is the production Store. It uses GET/SET with EXPIRE so
// every read is O(1) and the 30-day TTL refreshes on each successful
// write.
//
// In-process writes against the same chatID are serialized through a
// striped mutex (writeStripes) so a DAG-parallel tool dispatch issuing
// two memory_write calls, or two concurrent tabs on the same chat, do
// not silently lose each other's notes via a non-atomic GET-modify-SET.
// Cross-replica races (the same chat hitting two go-server instances
// in the same window) are still possible but vanishingly rare given
// WebSocket affinity; if they become a real concern, the fix is a Lua
// script doing the GET-modify-SET inside Redis.
//
// Cross-system isolation note: AP-D1 long-term memory writes
// (post-response runLongmemExtract) target a Postgres user_memory
// table via internal/longmem, not Redis. The two stores never share
// a key; there is no race between a sessionmem.AppendNote and a
// concurrent longmem.Insert.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration

	// writeStripes serializes in-process read-modify-write cycles for
	// the same chatID. 256 stripes keeps per-store memory below 4 KiB
	// while making same-stripe collisions between unrelated chats rare
	// at any plausible concurrent-chat count.
	writeStripes [redisWriteStripes]sync.Mutex
}

const redisWriteStripes = 256

// NewRedisStore returns a RedisStore using the given client. TTL
// defaults to DefaultTTL when zero.
func NewRedisStore(c *redis.Client, ttl time.Duration) *RedisStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &RedisStore{client: c, ttl: ttl}
}

// lockFor returns the stripe mutex that guards in-process writes for
// chatID. Stripe selection uses FNV-1a so callers don't need to think
// about distribution.
func (s *RedisStore) lockFor(chatID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(chatID))
	return &s.writeStripes[h.Sum32()%uint32(len(s.writeStripes))]
}

func key(chatID string) string {
	return "chat_session:" + chatID
}

// Get reads the session blob. Returns a zero-valued SessionMemory when
// the key is absent (or Redis is misconfigured) — callers must NOT
// branch on a missing session as a different state from an empty one.
func (s *RedisStore) Get(ctx context.Context, chatID string) (SessionMemory, error) {
	if s == nil || s.client == nil {
		return SessionMemory{}, nil
	}
	raw, err := s.client.Get(ctx, key(chatID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return SessionMemory{}, nil
	}
	if err != nil {
		return SessionMemory{}, fmt.Errorf("sessionmem: redis get: %w", err)
	}
	var m SessionMemory
	if err := json.Unmarshal(raw, &m); err != nil {
		// A corrupt blob is treated as empty rather than fatal — the
		// alternative would be to fail every chat in this session
		// indefinitely. Caller writes will overwrite the bad state.
		return SessionMemory{}, nil
	}
	return m, nil
}

// AppendNote loads-modifies-saves the session blob with a new note
// appended. The MaxNotes ring is enforced here (FIFO eviction).
// The MaxNoteChars cap is enforced at the boundary so callers that
// pass user input trivially fail closed when the user pastes a wall
// of text.
//
// In-process concurrent calls for the same chatID are serialized via
// a per-chatID stripe mutex (see RedisStore docstring) so the GET-SET
// pair cannot lose writes between two goroutines on this replica.
func (s *RedisStore) AppendNote(ctx context.Context, chatID string, note SessionNote) error {
	if s == nil || s.client == nil {
		return nil
	}
	if len([]byte(note.Text)) > MaxNoteChars {
		return ErrNoteTooLong
	}
	if note.AddedAt.IsZero() {
		note.AddedAt = time.Now()
	}
	lk := s.lockFor(chatID)
	lk.Lock()
	defer lk.Unlock()
	m, _ := s.Get(ctx, chatID)
	m.Notes = append(m.Notes, note)
	if len(m.Notes) > MaxNotes {
		m.Notes = m.Notes[len(m.Notes)-MaxNotes:]
	}
	m.UpdatedAt = time.Now()
	return s.put(ctx, chatID, m)
}

// AppendFinding is the same shape as AppendNote for FindingRef.
// Shares the stripe mutex with AppendNote so the two cannot race on
// the same chatID's blob.
func (s *RedisStore) AppendFinding(ctx context.Context, chatID string, ref FindingRef) error {
	if s == nil || s.client == nil {
		return nil
	}
	if ref.AddedAt.IsZero() {
		ref.AddedAt = time.Now()
	}
	lk := s.lockFor(chatID)
	lk.Lock()
	defer lk.Unlock()
	m, _ := s.Get(ctx, chatID)
	m.Findings = append(m.Findings, ref)
	if len(m.Findings) > MaxNotes {
		m.Findings = m.Findings[len(m.Findings)-MaxNotes:]
	}
	m.UpdatedAt = time.Now()
	return s.put(ctx, chatID, m)
}

// Delete removes the session blob entirely. Intended for explicit
// "forget this conversation" user actions; the TTL handles the
// passive case.
func (s *RedisStore) Delete(ctx context.Context, chatID string) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Del(ctx, key(chatID)).Err()
}

func (s *RedisStore) put(ctx context.Context, chatID string, m SessionMemory) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("sessionmem: marshal: %w", err)
	}
	return s.client.Set(ctx, key(chatID), raw, s.ttl).Err()
}

// ----- Per-turn write counter --------------------------------------------

// WriteCounter is the rate-limit gate for memory_write. The chat
// orchestrator owns one counter per turn; each call to Inc returns
// false once MaxWritesPerTurn writes have already been recorded. The
// counter is an atomic int so concurrent writes (e.g. a tool-using LLM
// that issues two memory_write calls before the first returns) are
// still bounded. Over-increments past MaxWritesPerTurn are harmless —
// the counter is per-turn and discarded after the turn ends.
type WriteCounter struct {
	n atomic.Int32
}

// NewWriteCounter returns a zero counter ready to track one chat turn.
func NewWriteCounter() *WriteCounter { return &WriteCounter{} }

// writeCounterCtxKey is the context value key used to thread a per-turn
// counter from the chat handler down to the memory_write tool. Unexported
// so external packages must go through WithWriteCounter / FromContext.
type writeCounterCtxKey struct{}

// WithWriteCounter attaches a fresh per-turn WriteCounter to the
// context. The chat handler creates one at the start of each chat turn
// (one per chatID, scoped to the request); the memory_write builtin
// looks it up via FromContext. Callers that don't attach a counter
// (tests, eval harness) get no rate limit — that's intentional, the
// rate limit is a production-only protection against an LLM that emits
// hundreds of memory_write calls in a single response.
func WithWriteCounter(ctx context.Context, c *WriteCounter) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, writeCounterCtxKey{}, c)
}

// FromContext returns the per-turn counter previously stored on ctx,
// or nil when none is attached. Callers MUST treat nil as "no rate
// limit" rather than as an error — production attaches one, test code
// often does not.
func FromContext(ctx context.Context) *WriteCounter {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(writeCounterCtxKey{}).(*WriteCounter)
	return c
}

// Inc increments the counter and returns false when the per-turn limit
// is exceeded. Callers must propagate the returned `allow` to the
// caller path (the tool handler must NOT write to Redis when allow is
// false). The unconditional Add is intentional: a racing pair of
// memory_write calls may both observe MaxWritesPerTurn+1, but the
// counter is turn-scoped so the inflated value is discarded with the
// turn.
func (w *WriteCounter) Inc() bool {
	if w == nil {
		return true
	}
	return w.n.Add(1) <= MaxWritesPerTurn
}

// Count returns the current write count. Intended for trajectory /
// metric emission, not for control flow — use Inc for that.
func (w *WriteCounter) Count() int {
	if w == nil {
		return 0
	}
	return int(w.n.Load())
}

// ----- In-memory store (test fixtures) -----------------------------------

// InMemoryStore is the test-time Store. Concurrency-safe for
// integration test fixtures where the orchestrator and the rate
// limiter could in principle race.
type InMemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]SessionMemory
}

// NewInMemoryStore returns an empty store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{sessions: make(map[string]SessionMemory)}
}

// Get satisfies Store.
func (s *InMemoryStore) Get(_ context.Context, chatID string) (SessionMemory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[chatID], nil
}

// AppendNote satisfies Store.
func (s *InMemoryStore) AppendNote(_ context.Context, chatID string, note SessionNote) error {
	if len([]byte(note.Text)) > MaxNoteChars {
		return ErrNoteTooLong
	}
	if note.AddedAt.IsZero() {
		note.AddedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.sessions[chatID]
	m.Notes = append(m.Notes, note)
	if len(m.Notes) > MaxNotes {
		m.Notes = m.Notes[len(m.Notes)-MaxNotes:]
	}
	m.UpdatedAt = time.Now()
	s.sessions[chatID] = m
	return nil
}

// AppendFinding satisfies Store.
func (s *InMemoryStore) AppendFinding(_ context.Context, chatID string, ref FindingRef) error {
	if ref.AddedAt.IsZero() {
		ref.AddedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.sessions[chatID]
	m.Findings = append(m.Findings, ref)
	if len(m.Findings) > MaxNotes {
		m.Findings = m.Findings[len(m.Findings)-MaxNotes:]
	}
	m.UpdatedAt = time.Now()
	s.sessions[chatID] = m
	return nil
}

// Delete satisfies Store.
func (s *InMemoryStore) Delete(_ context.Context, chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, chatID)
	return nil
}

// FormatPrompt renders a SessionMemory into the `Conversation memory:`
// system-prompt block the orchestrator prepends at turn start. The
// caller is responsible for joining this with the rest of the system
// prompt; we keep the formatting in one place so a wording tweak
// doesn't require touching every orchestrator.
func FormatPrompt(m SessionMemory, lang string, maxNotes int) string {
	if maxNotes <= 0 {
		maxNotes = 10
	}
	if len(m.Notes) == 0 {
		return ""
	}
	notes := m.Notes
	if len(notes) > maxNotes {
		notes = notes[len(notes)-maxNotes:]
	}
	header := "Conversation memory (durable facts from earlier turns):\n"
	if lang == "de" {
		header = "Konversations-Gedächtnis (dauerhafte Fakten aus früheren Runden):\n"
	}
	var b strings.Builder
	b.WriteString(header)
	for _, n := range notes {
		b.WriteString("- ")
		b.WriteString(n.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
