// Package chatattach holds documents a user uploads INTO a chat turn for
// comparison against a KB. These are parsed in memory and never ingested into
// the KB. Storage is session-scoped with its own TTL (distinct from
// internal/sessionmem, which holds small per-chat notes).
package chatattach

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotFound is returned when an attachment id is unknown or expired.
var ErrNotFound = errors.New("chatattach: attachment not found")

// Finding is one comparison result produced by the comparison engine.
type Finding struct {
	Mode         string   `json:"mode"`     // "contradiction" | "formal" | "completeness"
	Severity     string   `json:"severity"` // "high" | "medium" | "low"
	SectionIdx   int      `json:"sectionIdx"`
	UploadQuote  string   `json:"uploadQuote"`
	Issue        string   `json:"issue"`
	CitedFileIDs []string `json:"citedFileIds"`
	CitedQuote   string   `json:"citedQuote"`
}

// Attachment is a parsed uploaded document held for a chat session.
type Attachment struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	KbID      string    `json:"kbId"`
	Filename  string    `json:"filename"`
	MimeType  string    `json:"mimeType"`
	FullText  string    `json:"fullText"`
	Sections  []string  `json:"sections"`
	Findings  []Finding `json:"findings,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store persists parsed chat attachments.
type Store interface {
	Put(ctx context.Context, att Attachment) (string, error)
	Get(ctx context.Context, id string) (Attachment, error)
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("chatattach: newID: %w", err)
	}
	return "att_" + hex.EncodeToString(b[:]), nil
}

// InMemoryStore is a TTL map used in tests and single-process dev.
type InMemoryStore struct {
	ttl time.Duration
	mu  sync.RWMutex
	m   map[string]entry
}

type entry struct {
	att     Attachment
	expires time.Time
}

func NewInMemoryStore(ttl time.Duration) *InMemoryStore {
	return &InMemoryStore{ttl: ttl, m: make(map[string]entry)}
}

func (s *InMemoryStore) Put(_ context.Context, att Attachment) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if att.ID == "" {
		id, err := newID()
		if err != nil {
			return "", err
		}
		att.ID = id
	}
	if att.CreatedAt.IsZero() {
		att.CreatedAt = time.Now()
	}
	s.m[att.ID] = entry{att: att, expires: time.Now().Add(s.ttl)}
	return att.ID, nil
}

func (s *InMemoryStore) Get(_ context.Context, id string) (Attachment, error) {
	s.mu.RLock()
	e, ok := s.m[id]
	s.mu.RUnlock()
	if !ok {
		return Attachment{}, ErrNotFound
	}
	if time.Now().After(e.expires) {
		s.mu.Lock()
		delete(s.m, id)
		s.mu.Unlock()
		return Attachment{}, ErrNotFound
	}
	return e.att, nil
}
