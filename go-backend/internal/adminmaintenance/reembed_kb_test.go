package adminmaintenance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
)

func TestReembedKB_UsesKBAccessOverride(t *testing.T) {
	store := &fakeStore{
		files: map[string][]kb.FileReembedRow{
			"kb1": {
				{ID: "f1", KbID: "kb1", StoragePath: storagePtr("p/1"), Name: "a", Type: "pdf"},
			},
		},
	}
	enq := &fakeEnqueuer{}
	h := NewHandler(store, enq, nil)

	// Path says "WRONG" but context carries an Access with KB.ID = "kb1".
	// The handler should use the context KB ID, not the path value.
	req := httptest.NewRequest(http.MethodPost, "/api/kb/WRONG/reembed", nil)
	req.SetPathValue("id", "WRONG")
	access := &kbaccess.KBAccessResult{
		KB:   &kbaccess.KnowledgeBase{ID: "kb1"},
		Role: kbaccess.RoleEdit,
	}
	req = req.WithContext(kbaccess.WithAccess(req.Context(), access))
	rec := httptest.NewRecorder()

	h.ReembedKB(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if len(enq.enqueued) == 0 {
		t.Fatal("expected at least one enqueue call; got 0 — handler likely used path id WRONG instead of context KB id kb1")
	}
}

func TestReembedKB_QueuesReembeddableFiles(t *testing.T) {
	store := &fakeStore{
		files: map[string][]kb.FileReembedRow{
			"kb1": {
				{ID: "f1", KbID: "kb1", StoragePath: storagePtr("p/1"), Name: "a", Type: "pdf"},
				{ID: "f2", KbID: "kb1", StoragePath: nil, Name: "b", Type: "pdf"}, // skipped: no path
				{ID: "f3", KbID: "kb1", StoragePath: storagePtr("p/3"), Name: "c", Type: "pdf"},
			},
		},
	}
	enq := &fakeEnqueuer{}
	h := NewHandler(store, enq, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/reembed", nil)
	req.SetPathValue("id", "kb1")
	rec := httptest.NewRecorder()

	h.ReembedKB(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Queued != 2 {
		t.Fatalf("queued = %d, want 2 (f2 skipped: no storage path)", body.Queued)
	}
	if got, want := len(enq.enqueued), 2; got != want {
		t.Fatalf("enqueue calls = %d, want 2", got)
	}
}
