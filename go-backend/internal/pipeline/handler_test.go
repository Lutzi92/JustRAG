package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeStore struct {
	overrides map[string]*string
	err       error
}

func (f fakeStore) ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error) {
	return f.overrides, f.err
}

func newTestHandler(overrides map[string]*string, globals map[string]string) *Handler {
	return NewHandler(fakeStore{overrides: overrides}, fakeReader{vals: globals})
}

func doGet(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.GetWorkflow(rec, req)
	return rec
}

func TestGetWorkflowDefaultsToComplexLane(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Lane != LaneComplex {
		t.Fatalf("Lane = %q, want %q", g.Lane, LaneComplex)
	}
	if len(g.Nodes) == 0 {
		t.Fatal("projection returned no nodes")
	}
}

func TestGetWorkflowHonoursLaneParam(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow?lane=lookup")

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Lane != LaneLookup {
		t.Fatalf("Lane = %q, want %q", g.Lane, LaneLookup)
	}
}

func TestGetWorkflowRejectsUnknownLane(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow?lane=nonsense")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A KB override must be visible through the overlay.
//
// Two corrections from the brief's original draft, which toggled
// NodeFactuality via "chat_factuality_verifier_enabled":
//
//  1. NodeFactuality's actual Keys[0] is "factcheck_in_chat" — the real
//     master gate for post-response factchecking (internal/chat/siteconfig.go:224,
//     default true), changed during Task 5 after this brief was written.
//  2. Unlike Project()'s own unit tests (project_test.go), which pass a plain
//     fakeReader as both `r` and `global` and so bypass per-KB-registry
//     membership entirely, this handler builds a REAL
//     siteconfig.KBOverlayReader (siteconfig.NewKBOverlay) over the store's
//     overrides — exactly like production. KBOverlayReader only applies an
//     override for a key where siteconfig.IsPerKB(key) is true; it silently
//     falls through to the global value otherwise. "factcheck_in_chat" is
//     NOT in the per-KB registry yet (verification nodes are Editable: false
//     until Phase 2 — see the brief's Design point / self-review), so keying
//     this test on it would exercise a code path that can never fire in
//     production and the test would fail for a reason that has nothing to do
//     with a bug in the handler.
//
// So this test uses NodeCRAGGrade / "crag_enabled" instead: it is
// NodeCRAGGrade's Keys[0] AND it is a real per-KB-registry entry
// (internal/siteconfig/registry.go), so the KB override actually survives
// the overlay — this is the genuine "does a KB admin's override change what
// the KB projects?" path the test is meant to cover.
func TestGetWorkflowAppliesKBOverride(t *testing.T) {
	on := "true"
	h := newTestHandler(
		map[string]*string{"crag_enabled": &on},
		map[string]string{"crag_enabled": "false"},
	)

	rec := doGet(t, h, "/api/kb/kb-1/workflow")

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := nodeByIDIn(&g, NodeCRAGGrade)
	if n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q — the KB override was not applied",
			n.Activation, ActivationActive)
	}
	if n.Origins["crag_enabled"] != "kb" {
		t.Fatalf("Origins = %q, want %q",
			n.Origins["crag_enabled"], "kb")
	}
}
