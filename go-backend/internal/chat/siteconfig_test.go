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

func TestChatDriftReaders_Defaults(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	ctx := context.Background()
	if ChatDriftEnabled(ctx, r) {
		t.Errorf("ChatDriftEnabled default: want false")
	}
	if got := ChatDriftMaxFollowups(ctx, r); got != 4 {
		t.Errorf("ChatDriftMaxFollowups default: want 4, got %d", got)
	}
	if got := ChatDriftPrimerTopK(ctx, r); got != 6 {
		t.Errorf("ChatDriftPrimerTopK default: want 6, got %d", got)
	}
	if got := ChatDriftSearchTopK(ctx, r); got != 8 {
		t.Errorf("ChatDriftSearchTopK default: want 8, got %d", got)
	}
}

func TestChatDriftReaders_SetAndOutOfRange(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_drift_enabled":       strPtr("true"),
		"chat_drift_max_followups": strPtr("99"), // > hi=8 → out of range → default 4
		"chat_drift_primer_top_k":  strPtr("0"),  // < lo=1 → out of range → default 6
		"chat_drift_search_top_k":  strPtr("12"), // in [1,30] → 12
	}}
	ctx := context.Background()
	if !ChatDriftEnabled(ctx, r) {
		t.Errorf("ChatDriftEnabled: want true")
	}
	if got := ChatDriftMaxFollowups(ctx, r); got != 4 {
		t.Errorf("out-of-range max_followups → default 4, got %d", got)
	}
	if got := ChatDriftPrimerTopK(ctx, r); got != 6 {
		t.Errorf("out-of-range primer_top_k → default 6, got %d", got)
	}
	if got := ChatDriftSearchTopK(ctx, r); got != 12 {
		t.Errorf("search_top_k: want 12, got %d", got)
	}
}
