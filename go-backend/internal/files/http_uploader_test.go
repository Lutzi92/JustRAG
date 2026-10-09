package files_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/files"
)

func TestFetchURL_RecordsUploader(t *testing.T) {
	defer files.StubFetchForTest("<html>hello</html>", "text/html")()

	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url",
		strings.NewReader(`{"url":"https://example.com/page","title":"Page"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withKBAccess(withUser(req, ingestUser()), kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 1 || store.created[0].UploadedBy != ingestUser().ID {
		t.Fatalf("CreateFile got %+v, want UploadedBy=%q", store.created, ingestUser().ID)
	}
}

func TestAddSources_RecordsUploader(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/add-sources",
		strings.NewReader(`{"pages":[{"title":"A","url":"https://example.com/a","content":"one"},{"title":"B","url":"https://example.com/b","content":"two"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withKBAccess(withUser(req, ingestUser()), kb)

	rr := httptest.NewRecorder()
	h.AddSources(rr, req)

	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("expected 2xx, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 2 {
		t.Fatalf("expected 2 CreateFile calls, got %d", len(store.created))
	}
	for _, c := range store.created {
		if c.UploadedBy != ingestUser().ID {
			t.Fatalf("CreateFile got %+v, want UploadedBy=%q", c, ingestUser().ID)
		}
	}
}
