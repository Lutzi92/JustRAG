package academic_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/academic"
)

func TestAddPapers_RecordsUploader(t *testing.T) {
	defer academic.AllowAllURLsForTest()()

	pdfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 fake content"))
	}))
	defer pdfSrv.Close()

	store := newStubStore(true, "http://justfind.example.com")
	h := academic.NewHandler(store, nil, nil, newStubStorage(), nil, nil)

	bodyBytes, _ := json.Marshal(map[string]any{
		"kbId": "kb-1",
		"papers": []map[string]any{
			{"id": "paper-1", "title": "Deep Learning Survey", "pdfUrl": pdfSrv.URL + "/paper.pdf"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/academic-research/r1/papers/add", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("researchId", "r1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.AddPapers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(store.created) != 1 || store.created[0].UploadedBy != "user-1" {
		t.Fatalf("CreateFile got %+v, want UploadedBy=user-1", store.created)
	}
}
