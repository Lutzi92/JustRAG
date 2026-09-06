package builtin

import (
	"os"
	"strings"
	"testing"
)

// Must stay byte-identical to vector.effectiveDateExpr: the retrieval window
// filter (internal/vector) and this listing have to select the same files, or
// the deterministic recency-listing path injects a file list that disagrees
// with the chunks retrieval actually returned.
//
// Mutation: change either constant → this fails.
func TestRecentDocuments_EffectiveDateExpr(t *testing.T) {
	const want = "COALESCE(published_at, created_at)"
	if effectiveDateExpr != want {
		t.Errorf("effectiveDateExpr = %q, want %q", effectiveDateExpr, want)
	}
}

// Drift alarm: both queries (RecentDocuments, NameMarkerDocuments) must build
// their select/filter/order from the constant, never from a pasted column
// name.
//
// Mutation: put `ORDER BY created_at DESC` back into either query → fails.
func TestRecentDocuments_QueriesUseTheSharedExpression(t *testing.T) {
	src, err := os.ReadFile("recent_documents.go")
	if err != nil {
		t.Fatalf("read recent_documents.go: %v", err)
	}
	var offenders []string
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "created_at") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "const effectiveDateExpr") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		offenders = append(offenders, trimmed)
	}
	if len(offenders) > 0 {
		t.Errorf("recent_documents.go references created_at outside effectiveDateExpr:\n  %s",
			strings.Join(offenders, "\n  "))
	}
	if !strings.Contains(string(src), "const effectiveDateExpr") {
		t.Fatal("drift alarm: effectiveDateExpr constant not found in recent_documents.go")
	}
}
