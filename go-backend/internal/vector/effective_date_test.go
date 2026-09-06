package vector

import (
	"os"
	"strings"
	"testing"
)

// The one expression every file-date read must go through. Migration 0071
// added files.published_at (RSS only, NULL elsewhere), so "the date of a
// document" is its publication date when it has one and its ingest
// timestamp otherwise.
const wantEffectiveDateExpr = "COALESCE(published_at, created_at)"

// Mutation: set effectiveDateExpr back to "created_at" → this fails. That is
// the whole point of the constant — a published_at-aware window filter and a
// created_at-keyed recency boost would silently disagree about which files
// are recent.
func TestEffectiveDateExpr_CoalescesPublishedAt(t *testing.T) {
	if effectiveDateExpr != wantEffectiveDateExpr {
		t.Errorf("effectiveDateExpr = %q, want %q", effectiveDateExpr, wantEffectiveDateExpr)
	}
}

// TestRecencyBoostSQL_UsesTheSharedExpression is the drift alarm for the
// sibling reads in this file: fileCreatedTimes (the recency boost) and
// fileIDsInDateRange (the date-window filter) must both build their SQL from
// effectiveDateExpr, never from a pasted column name.
//
// Mutation: inline `created_at` into either query (e.g.
// `SELECT id::text, created_at FROM files ...`) → this fails, because the
// only place the bare column name may appear in this file is inside the
// constant itself.
func TestRecencyBoostSQL_UsesTheSharedExpression(t *testing.T) {
	src, err := os.ReadFile("recency_boost.go")
	if err != nil {
		t.Fatalf("read recency_boost.go: %v", err)
	}
	var offenders []string
	for i, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "created_at") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		// The constant declaration itself, and comments, are allowed.
		if strings.HasPrefix(trimmed, "const effectiveDateExpr") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		offenders = append(offenders, strings.TrimSpace(line)+"  (line "+itoa(i+1)+")")
	}
	if len(offenders) > 0 {
		t.Errorf("recency_boost.go references created_at outside effectiveDateExpr:\n  %s",
			strings.Join(offenders, "\n  "))
	}
	// Guard against a vacuous pass: if the file stopped containing the
	// constant at all, the loop above would find nothing and report success.
	if !strings.Contains(string(src), "const effectiveDateExpr") {
		t.Fatal("drift alarm: effectiveDateExpr constant not found in recency_boost.go")
	}
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
