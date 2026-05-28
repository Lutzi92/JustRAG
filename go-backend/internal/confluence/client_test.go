package confluence_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/confluence"
)

func TestGetAllSpacePagesWithAncestors_PaginatesAndParses(t *testing.T) {
	calls := 0
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/content") {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("expand"); got != "ancestors" {
			t.Errorf("expected expand=ancestors only, got %q", got)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch calls {
		case 1:
			w.Write([]byte(`{
				"results":[
					{"id":"1","title":"Root","ancestors":[]},
					{"id":"2","title":"Child","ancestors":[{"id":"1","title":"Root"}]}
				],
				"_links":{"next":"/rest/api/content?start=2"}
			}`))
		case 2:
			w.Write([]byte(`{
				"results":[
					{"id":"3","title":"Grandchild","ancestors":[{"id":"1","title":"Root"},{"id":"2","title":"Child"}]}
				]
			}`))
		default:
			t.Fatalf("unexpected extra call %d", calls)
		}
	}))
	defer fakeCF.Close()

	c := confluence.NewConfluenceClient(fakeCF.URL, "tok")
	pages, err := c.GetAllSpacePagesWithAncestors(context.Background(), "ENG")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("expected 3 pages, got %d", len(pages))
	}
	if pages[0].ID != "1" || pages[0].Title != "Root" {
		t.Errorf("page 0: got %+v", pages[0])
	}
	if len(pages[0].AncestorTitles) != 0 {
		t.Errorf("page 0 expected 0 ancestors, got %v", pages[0].AncestorTitles)
	}
	if got := pages[1].AncestorTitles; len(got) != 1 || got[0] != "Root" {
		t.Errorf("page 1 ancestors: got %v want [Root]", got)
	}
	if got := pages[2].AncestorTitles; len(got) != 2 || got[0] != "Root" || got[1] != "Child" {
		t.Errorf("page 2 ancestors: got %v want [Root Child]", got)
	}

	raw, _ := json.Marshal(pages[2])
	if !strings.Contains(string(raw), `"ancestorTitles"`) {
		t.Errorf("expected ancestorTitles json tag, got %s", raw)
	}

	// Root pages must marshal ancestorTitles as [] (not null) so the
	// frontend can call array methods without a nullcheck. A nil slice
	// here would crash the import-modal search filter.
	rootRaw, _ := json.Marshal(pages[0])
	if !strings.Contains(string(rootRaw), `"ancestorTitles":[]`) {
		t.Errorf("root page must marshal ancestorTitles as [], got %s", rootRaw)
	}
}

func TestGetAllSpacePagesWithAncestors_ReturnsErrorOn500(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer fakeCF.Close()

	c := confluence.NewConfluenceClient(fakeCF.URL, "tok")
	_, err := c.GetAllSpacePagesWithAncestors(context.Background(), "ENG")
	if err == nil {
		t.Fatalf("expected error from 500 response, got nil")
	}
}
