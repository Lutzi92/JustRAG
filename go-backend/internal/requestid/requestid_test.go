package requestid

import (
	"context"
	"testing"
)

func TestFromContext_Empty(t *testing.T) {
	if got := FromContext(context.Background()); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestNewContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "abc123")
	if got := FromContext(ctx); got != "abc123" {
		t.Fatalf("expected abc123, got %q", got)
	}
}

func TestEnsureContext_GeneratesWhenMissing(t *testing.T) {
	ctx, id, err := EnsureContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("expected generated id, got empty")
	}
	if got := FromContext(ctx); got != id {
		t.Fatalf("FromContext should match generated id, got %q vs %q", got, id)
	}
	if len(id) < 16 {
		t.Fatalf("generated id should be reasonably long, got %q", id)
	}
}

func TestEnsureContext_PreservesExisting(t *testing.T) {
	parent := NewContext(context.Background(), "existing")
	ctx, id, err := EnsureContext(parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "existing" {
		t.Fatalf("expected preserved id, got %q", id)
	}
	if got := FromContext(ctx); got != "existing" {
		t.Fatalf("FromContext should return preserved id, got %q", got)
	}
}
