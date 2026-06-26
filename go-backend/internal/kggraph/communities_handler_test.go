package kggraph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// communitiesEnabledFromReader mirrors community.Enabled without importing it:
// true iff kg_communities_enabled equals "true".
func communitiesEnabledFromReader(ctx context.Context, r CanonReader) bool {
	v, err := r.GetSiteConfigValue(ctx, "kg_communities_enabled")
	return err == nil && v != nil && *v == "true"
}

func postCommunities(t *testing.T, h *CommunitiesHandler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/communities/build", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.PostBuildCommunities(rec, req)
	return rec
}

func TestPostBuildCommunities_GateOff503NoEnqueue(t *testing.T) {
	enq := &fakeEnqueuer{}
	h := NewCommunitiesHandler(enq, canonMapReader{}, communitiesEnabledFromReader)
	rec := postCommunities(t, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("gate off: want 503, got %d", rec.Code)
	}
	if enq.calls != 0 {
		t.Errorf("gate off must not enqueue, got %d calls", enq.calls)
	}
}

func TestPostBuildCommunities_GateOnEnqueues202(t *testing.T) {
	enq := &fakeEnqueuer{}
	h := NewCommunitiesHandler(enq, canonMapReader{"kg_communities_enabled": "true"}, communitiesEnabledFromReader)
	rec := postCommunities(t, h)
	if rec.Code != http.StatusAccepted {
		t.Errorf("gate on: want 202, got %d", rec.Code)
	}
	if enq.calls != 1 {
		t.Errorf("gate on should enqueue once, got %d", enq.calls)
	}
}
