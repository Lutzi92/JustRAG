package adminkboverview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hibiken/asynq"
)

func TestOverviewHandler_ReturnsJSONShape(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{{ID: "kb-1", Name: "Alpha", IsPublished: true, CreatedAt: "2026-01-01T00:00:00Z"}},
		fileMap: map[string]FileStats{
			"kb-1": {FileCount: 3, TotalSizeBytes: 1024},
		},
		msgMap: map[string]MessageStats{
			"kb-1": {MessageCount: 9, ChatCount: 2},
		},
	}
	insp := &fakeInspector{info: map[string]*asynq.QueueInfo{
		"rag-quick": {Pending: 1},
	}}
	h := NewHandler(NewService(store, insp))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/kb-overview", nil)
	w := httptest.NewRecorder()
	h.Overview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp OverviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].FileCount != 3 || resp.Rows[0].MessageCount != 9 {
		t.Errorf("row payload wrong: %+v", resp.Rows)
	}
	if resp.QueueSummary["rag-quick"].Waiting != 1 {
		t.Errorf("queue summary not surfaced: %+v", resp.QueueSummary)
	}
	if resp.Timestamp == "" {
		t.Error("timestamp should be populated")
	}
}
