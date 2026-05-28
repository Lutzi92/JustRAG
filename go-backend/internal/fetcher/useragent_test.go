package fetcher

import (
	"testing"
)

func TestUserAgentPoolNotEmpty(t *testing.T) {
	t.Parallel()
	if len(userAgentPool) == 0 {
		t.Fatal("userAgentPool must not be empty")
	}
}

func TestRandomUserAgentReturnsFromPool(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ua := randomUserAgent()
		seen[ua] = true
		found := false
		for _, candidate := range userAgentPool {
			if ua == candidate {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("randomUserAgent returned %q which is not in userAgentPool", ua)
		}
	}
	if len(seen) < 2 {
		t.Errorf("expected multiple distinct UAs over 100 draws, got %d", len(seen))
	}
}

func TestResolveUserAgentRespectsOverride(t *testing.T) {
	t.Parallel()
	got := resolveUserAgent("CustomBot/1.0")
	if got != "CustomBot/1.0" {
		t.Errorf("expected override to win, got %q", got)
	}
}

func TestResolveUserAgentFallsBackToPool(t *testing.T) {
	t.Parallel()
	got := resolveUserAgent("")
	found := false
	for _, ua := range userAgentPool {
		if got == ua {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resolveUserAgent(\"\") returned %q not in pool", got)
	}
}
