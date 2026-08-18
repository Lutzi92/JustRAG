package kbinvites_test

import (
	"testing"

	"github.com/justrag/go-backend/internal/kbinvites"
)

func TestNewTokenIsURLSafeAndUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		tok, err := kbinvites.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if len(tok) != 43 {
			t.Fatalf("token length = %d, want 43 (32 raw bytes base64url)", len(tok))
		}
		for _, r := range tok {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Fatalf("token %q contains non-URL-safe rune %q", tok, r)
			}
		}
		if seen[tok] {
			t.Fatalf("NewToken returned a duplicate after %d draws", i)
		}
		seen[tok] = true
	}
}
