package adminfeedback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/feedback"
)

type fakeStore struct{ rows []feedback.NegativeChunk }

func (f fakeStore) TopNegative(_ context.Context, _ string, _ int) ([]feedback.NegativeChunk, error) {
	return f.rows, nil
}

func TestGetNegativeChunks(t *testing.T) {
	h := NewHandler(fakeStore{rows: []feedback.NegativeChunk{{ChunkID: "c1", NetSignal: -4, SignalCount: 6}}})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/feedback/chunks?limit=10", nil)
	w := httptest.NewRecorder()
	h.GetNegativeChunks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got []feedback.NegativeChunk
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].NetSignal != -4 {
		t.Fatalf("unexpected body: %+v", got)
	}
}
