package kggraph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hibiken/asynq"
)

type fakeEnqueuer struct {
	calls int
	err   error
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, _ *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	f.calls++
	return &asynq.TaskInfo{}, f.err
}

type canonMapReader map[string]string

func (m canonMapReader) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := m[k]; ok {
		return &v, nil
	}
	return nil, nil
}

// enabledFromReader mirrors canonicalize.Enabled without importing it: true iff
// the key equals "true".
func enabledFromReader(ctx context.Context, r CanonReader) bool {
	v, err := r.GetSiteConfigValue(ctx, "kg_canonicalization_enabled")
	return err == nil && v != nil && *v == "true"
}

func postCanon(t *testing.T, h *CanonicalizeHandler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/canonicalize", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.PostCanonicalize(rec, req)
	return rec
}

func TestPostCanonicalize_GateOff503NoEnqueue(t *testing.T) {
	enq := &fakeEnqueuer{}
	h := NewCanonicalizeHandler(enq, canonMapReader{}, enabledFromReader)
	rec := postCanon(t, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("gate off: want 503, got %d", rec.Code)
	}
	if enq.calls != 0 {
		t.Errorf("gate off must not enqueue, got %d calls", enq.calls)
	}
}

func TestPostCanonicalize_GateOnEnqueues202(t *testing.T) {
	enq := &fakeEnqueuer{}
	h := NewCanonicalizeHandler(enq, canonMapReader{"kg_canonicalization_enabled": "true"}, enabledFromReader)
	rec := postCanon(t, h)
	if rec.Code != http.StatusAccepted {
		t.Errorf("gate on: want 202, got %d", rec.Code)
	}
	if enq.calls != 1 {
		t.Errorf("gate on should enqueue once, got %d", enq.calls)
	}
}
