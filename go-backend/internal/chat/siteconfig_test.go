package chat

import (
	"context"
	"testing"
)

// These tests lock the chat_compare_* site_config accessor defaults.
// They share fakeSiteConfigReader with the rest of the chat package.

func TestCompareConfigDefaults(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	ctx := context.Background()
	if CompareEnabled(ctx, r) {
		t.Fatal("compare should default off")
	}
	if got := CompareMaxSections(ctx, r); got != 60 {
		t.Fatalf("max sections default = %d, want 60", got)
	}
	if got := CompareConcurrency(ctx, r); got != 6 {
		t.Fatalf("concurrency default = %d, want 6", got)
	}
	if got := ComparePeersPerSection(ctx, r); got != 5 {
		t.Fatalf("peers default = %d, want 5", got)
	}
	if got := CompareAttachmentTTLHours(ctx, r); got != 24 {
		t.Fatalf("ttl default = %d, want 24", got)
	}
	if got := CompareMaxFileBytes(ctx, r); got != 10485760 {
		t.Fatalf("max bytes default = %d, want 10485760", got)
	}
}
