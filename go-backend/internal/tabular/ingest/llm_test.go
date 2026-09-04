package ingest

import (
	"context"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// TestAIProfilerNilResolver covers round-1 item 4: a nil Resolver must
// return an error, not panic inside internal/ai (ai.ProfileTableRegion
// dereferences the resolver unconditionally).
func TestAIProfilerNilResolver(t *testing.T) {
	t.Parallel()
	p := &AIProfiler{KBID: "kb", Lang: "de"}
	_, err := p.ProfileTableRegion(context.Background(), ai.SheetProfileRequest{})
	if err == nil {
		t.Fatal("expected an error for a nil Resolver, got nil")
	}
	if !strings.Contains(err.Error(), "nil ai resolver") {
		t.Errorf("error = %q, want it to mention the nil resolver", err.Error())
	}
}
