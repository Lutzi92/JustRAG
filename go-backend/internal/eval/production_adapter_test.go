package eval

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeRecencyLister is a minimal chat.RecencyLister satisfying test double;
// none of its methods need to do anything for this test since buildParams
// only checks that a non-nil lister reaches ChatContextParams.
type fakeRecencyLister struct{}

func (fakeRecencyLister) RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]chat.RecencyDoc, error) {
	return nil, nil
}

func (fakeRecencyLister) DocumentsWithNameMarker(ctx context.Context, kbID, nameRegex string, limit int) ([]chat.RecencyDoc, error) {
	return nil, nil
}

// TestBuildParams_RecencyListerAndDateLineWired asserts that
// WithRecencyLister reaches chat.ChatContextParams.RecencyLister and that
// CurrentDateLine is populated when chat_date_awareness_enabled is on
// (default). Wave 2 Task 8: without this wiring, the CERT recency fixture's
// headline mechanism (window-scoped retrieval + the file-listing addendum)
// never fires under cmd/eval even though the question runs and "succeeds" —
// a silent eval/production divergence, not a crash. Mutation: dropping the
// RecencyLister or CurrentDateLine assignment in buildParams fails this
// test.
func TestBuildParams_RecencyListerAndDateLineWired(t *testing.T) {
	lister := fakeRecencyLister{}
	siteCfg := &stubSiteCfg{values: map[string]string{}}
	a := NewProductionContextAdapter(
		(*ai.ConfigResolver)(nil),
		(vector.Searcher)(nil),
		siteCfg,
		EvalFlags{},
		WithRecencyLister(lister),
	)

	params := a.buildParams(context.Background(), Question{ID: "q1", KbID: "kb-1", Language: "de"}, "query text", "")

	if params.RecencyLister == nil {
		t.Fatal("expected params.RecencyLister to be set, got nil")
	}
	if params.CurrentDateLine == "" {
		t.Fatal("expected params.CurrentDateLine to be non-empty with chat_date_awareness_enabled defaulting on")
	}
}

// TestBuildParams_NoRecencyListerByDefault asserts that omitting
// WithRecencyLister leaves params.RecencyLister nil, so a
// --production-context run without the option reproduces the pre-Task-8
// pipeline exactly (no window scoping, no listing addendum) — the
// retrieval-only ablation branches in cmd/eval depend on this.
func TestBuildParams_NoRecencyListerByDefault(t *testing.T) {
	siteCfg := &stubSiteCfg{values: map[string]string{}}
	a := NewProductionContextAdapter(
		(*ai.ConfigResolver)(nil),
		(vector.Searcher)(nil),
		siteCfg,
		EvalFlags{},
	)

	params := a.buildParams(context.Background(), Question{ID: "q1", KbID: "kb-1", Language: "de"}, "query text", "")

	if params.RecencyLister != nil {
		t.Fatalf("expected params.RecencyLister to stay nil without WithRecencyLister, got %v", params.RecencyLister)
	}
}

// TestBuildParams_NoDateLineWhenAwarenessDisabled asserts CurrentDateLine
// stays empty when chat_date_awareness_enabled is explicitly off, mirroring
// chat.SystemPromptDateLine's own kill-switch contract.
func TestBuildParams_NoDateLineWhenAwarenessDisabled(t *testing.T) {
	siteCfg := &stubSiteCfg{values: map[string]string{"chat_date_awareness_enabled": "false"}}
	a := NewProductionContextAdapter(
		(*ai.ConfigResolver)(nil),
		(vector.Searcher)(nil),
		siteCfg,
		EvalFlags{},
		WithRecencyLister(fakeRecencyLister{}),
	)

	params := a.buildParams(context.Background(), Question{ID: "q1", KbID: "kb-1", Language: "de"}, "query text", "")

	if params.CurrentDateLine != "" {
		t.Fatalf("expected empty CurrentDateLine with chat_date_awareness_enabled=false, got %q", params.CurrentDateLine)
	}
}
