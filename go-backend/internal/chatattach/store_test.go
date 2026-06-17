package chatattach

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInMemoryStorePutGet(t *testing.T) {
	s := NewInMemoryStore(time.Hour)
	att := Attachment{
		UserID:   "u1",
		KbID:     "kb1",
		Filename: "module.md",
		MimeType: "text/markdown",
		FullText: "# Modul A\nECTS: 6",
		Sections: []string{"# Modul A", "ECTS: 6"},
	}
	id, err := s.Put(context.Background(), att)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}
	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Filename != "module.md" || len(got.Sections) != 2 || got.UserID != "u1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestInMemoryStoreGetMissing(t *testing.T) {
	s := NewInMemoryStore(time.Hour)
	if _, err := s.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestInMemoryStorePutKeepsSuppliedID(t *testing.T) {
	s := NewInMemoryStore(time.Hour)
	id, err := s.Put(context.Background(), Attachment{ID: "att_fixed", UserID: "u1"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if id != "att_fixed" {
		t.Fatalf("expected supplied id to be kept, got %q", id)
	}
	got, err := s.Get(context.Background(), "att_fixed")
	if err != nil || got.UserID != "u1" {
		t.Fatalf("Get by supplied id failed: %v %+v", err, got)
	}
}

func TestInMemoryStoreExpiry(t *testing.T) {
	s := NewInMemoryStore(time.Nanosecond)
	id, _ := s.Put(context.Background(), Attachment{UserID: "u1"})
	time.Sleep(time.Millisecond)
	if _, err := s.Get(context.Background(), id); err != ErrNotFound {
		t.Fatalf("expected expired entry to be ErrNotFound, got %v", err)
	}
}

func TestRedisStoreRoundTripJSON(t *testing.T) {
	// Encode/decode is the risk surface; assert the JSON contract directly.
	att := Attachment{ID: "att_x", UserID: "u1", Sections: []string{"a", "b"},
		Findings: []Finding{{Mode: "formal", Severity: "high", SectionIdx: 1}}}
	b, err := encodeAttachment(att)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeAttachment(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != "u1" || len(got.Findings) != 1 || got.Findings[0].Mode != "formal" {
		t.Fatalf("json round-trip mismatch: %+v", got)
	}
}

func TestRedisStorePutGet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewRedisStore(rdb, time.Hour)

	id, err := s.Put(context.Background(), Attachment{UserID: "u1", KbID: "kb1", Sections: []string{"a"}})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != "u1" || got.KbID != "kb1" || len(got.Sections) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestRedisStoreGetMissing(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewRedisStore(rdb, time.Hour)
	if _, err := s.Get(context.Background(), "att_missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
