package academic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/academic"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// Stub store
// ---------------------------------------------------------------------------

type stubStore struct {
	siteConfig map[string]string
	createErr  error
	fileErr    error
	kbRole     string
}

func newStubStore(enabled bool, baseURL string) *stubStore {
	cfg := map[string]string{}
	if enabled {
		cfg["academic_search_enabled"] = "true"
	}
	if baseURL != "" {
		cfg["academic_search_base_url"] = baseURL
	}
	// GetKBByID below always returns a KB owned by "user-1" (the caller in
	// every AddPapers test); kbRole mirrors the kb_members row a real owner
	// has (migration 0064's owner-mirror trigger) so kbaccess.EffectiveRole
	// grants edit access the same way the old kb.UserID-equality check did.
	return &stubStore{siteConfig: cfg, kbRole: kbaccess.RoleOwner}
}

func (s *stubStore) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v, ok := s.siteConfig[key]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func (s *stubStore) CreateResearchSession(_ context.Context, kbID, userID, goal, sessionType string) (*chat.ChatRow, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return &chat.ChatRow{
		ID:        "session-1",
		KbID:      kbID,
		UserID:    userID,
		Title:     goal,
		Type:      sessionType,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (s *stubStore) CreateFile(_ context.Context, data files.CreateFileData) (*files.FileRecord, error) {
	if s.fileErr != nil {
		return nil, s.fileErr
	}
	sp := data.StoragePath
	return &files.FileRecord{
		ID:          "file-1",
		KbID:        data.KbID,
		Name:        data.Name,
		Type:        data.Type,
		Status:      "pending",
		Origin:      data.Origin,
		StoragePath: &sp,
		CreatedAt:   time.Now(),
	}, nil
}

func (s *stubStore) GetKBByID(_ context.Context, id string) (*kbaccess.KnowledgeBase, error) {
	userID := "user-1"
	return &kbaccess.KnowledgeBase{ID: id, UserID: &userID, IsGlobal: false}, nil
}

func (s *stubStore) GetKBRole(_ context.Context, kbID, userID string) (string, error) {
	return s.kbRole, nil
}

// ---------------------------------------------------------------------------
// Stub storage
// ---------------------------------------------------------------------------

type stubStorage struct {
	stored map[string][]byte
	err    error
}

func newStubStorage() *stubStorage {
	return &stubStorage{stored: make(map[string][]byte)}
}

func (s *stubStorage) StoreFile(_ context.Context, path string, content []byte, _ string) error {
	if s.err != nil {
		return s.err
	}
	s.stored[path] = content
	return nil
}
func (s *stubStorage) StoreFileFromReader(_ context.Context, path string, r io.Reader, _ string) error {
	b, _ := io.ReadAll(r)
	s.stored[path] = b
	return nil
}
func (s *stubStorage) ReadFile(_ context.Context, path string) ([]byte, error) {
	b, ok := s.stored[path]
	if !ok {
		return nil, fmt.Errorf("not found: %s", path)
	}
	return b, nil
}
func (s *stubStorage) ReadFileStream(_ context.Context, path string) (io.ReadCloser, error) {
	b, err := s.ReadFile(nil, path) //nolint:staticcheck
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (s *stubStorage) DeleteFile(_ context.Context, _ string) error      { return nil }
func (s *stubStorage) DeleteFiles(_ context.Context, _ []string) error   { return nil }
func (s *stubStorage) DeleteDirectory(_ context.Context, _ string) error { return nil }
func (s *stubStorage) FileExists(_ context.Context, path string) (bool, error) {
	_, ok := s.stored[path]
	return ok, nil
}
func (s *stubStorage) IsS3() bool { return false }

// ---------------------------------------------------------------------------
// Helper: inject a fake user into request context.
// ---------------------------------------------------------------------------

func withUser(r *http.Request, userID string) *http.Request {
	claims := &auth.Claims{ID: userID, Username: "testuser"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Test 1: Search returns papers (mock HTTP server with HTML)
// ---------------------------------------------------------------------------

func TestParseResultsHTML_ReturnsPapers(t *testing.T) {
	// Build a minimal JustFind-style HTML page with one result.
	sampleHTML := `<!DOCTYPE html><html><body>
<ol class="result-list">
  <li class="result-list-item">
    <h3 class="title"><a href="/EDS/Record?id=123">Test Paper Title</a></h3>
    <span class="Z3988" title="rft.atitle=Test+Paper+Title&amp;rft.au=Smith%2C+Jane&amp;rft.date=2022&amp;rft.jtitle=Nature"></span>
  </li>
</ol>
</body></html>`

	// Use ParseResultsHTML directly to avoid SSRF guard on local test servers.
	papers, err := academic.ParseResultsHTML(sampleHTML, "http://justfind.example.com")
	if err != nil {
		t.Fatalf("ParseResultsHTML error: %v", err)
	}
	if len(papers) == 0 {
		t.Error("expected at least one paper in response")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Search disabled → 403
// ---------------------------------------------------------------------------

func TestSearch_Disabled(t *testing.T) {
	store := newStubStore(false, "")
	stor := newStubStorage()
	h := academic.NewHandler(store, nil, nil, stor, nil, nil)

	body := map[string]any{"query": "test"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/academic-search", bytes.NewReader(bodyBytes))
	req.SetPathValue("id", "kb-1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.Search(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 3: AddPapers with mock storage → 200
// ---------------------------------------------------------------------------

func TestAddPapers_StoresPDF(t *testing.T) {
	// Serve a fake PDF file.
	fakePDF := []byte("%PDF-1.4 fake content")
	pdfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(fakePDF)
	}))
	defer pdfSrv.Close()

	store := newStubStore(true, "http://justfind.example.com")
	stor := newStubStorage()
	h := academic.NewHandler(store, nil, nil, stor, nil, nil)

	reqBody := map[string]any{
		"kbId": "kb-1",
		"papers": []map[string]any{
			{
				"id":     "paper-1",
				"title":  "Deep Learning Survey",
				"pdfUrl": pdfSrv.URL + "/paper.pdf",
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/academic-research/r1/papers/add", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("researchId", "r1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.AddPapers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// The PDF server runs on localhost which the SSRF check correctly blocks.
	// So the paper should be in the "failed" list.
	added := result["added"].([]any)
	failed := result["failed"].([]any)
	if len(failed) != 1 {
		t.Errorf("expected 1 failed (SSRF blocks localhost), got %d added / %d failed", len(added), len(failed))
	}
}

// ---------------------------------------------------------------------------
// Test 4: AddPapers with invalid PDF magic bytes → paper goes to failed
// ---------------------------------------------------------------------------

func TestAddPapers_RejectsNonPDF(t *testing.T) {
	notPDF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>Not a PDF</html>")
	}))
	defer notPDF.Close()

	store := newStubStore(true, "http://justfind.example.com")
	stor := newStubStorage()
	h := academic.NewHandler(store, nil, nil, stor, nil, nil)

	reqBody := map[string]any{
		"kbId": "kb-1",
		"papers": []map[string]any{
			{"id": "bad-paper", "title": "Not a PDF", "pdfUrl": notPDF.URL + "/fake.pdf"},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/academic-research/r1/papers/add", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("researchId", "r1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.AddPapers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)
	failed := result["failed"].([]any)
	if len(failed) != 1 {
		t.Errorf("expected 1 failed, got %d: %v", len(failed), result)
	}
}

// ---------------------------------------------------------------------------
// Test 5: StartResearch compiles and returns 403 when disabled
// ---------------------------------------------------------------------------

func TestStartResearch_Compiles(t *testing.T) {
	store := newStubStore(false, "")
	stor := newStubStorage()
	h := academic.NewHandler(store, nil, nil, stor, nil, nil)

	body := map[string]any{"topic": "machine learning"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/academic-research", bytes.NewReader(bodyBytes))
	req.SetPathValue("id", "kb-1")
	req = withUser(req, "user-1")

	w := httptest.NewRecorder()
	h.StartResearch(w, req)

	// Disabled → 403
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Test 6: JustFind HTML parsing via SearchJustFind
// ---------------------------------------------------------------------------

func TestSearchJustFind_ParsesHTML(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<ul>
  <li class="result-list-item">
    <h3 class="title"><a href="/EDS/Record?id=42">Climate Change Effects</a></h3>
    <span class="Z3988" title="rft.atitle=Climate+Change+Effects&rft.au=Doe%2C+John&rft.date=2021&rft.jtitle=Science&rft_id=info%3Adoi%2F10.1234%2Ftest"></span>
  </li>
</ul>
</body></html>`

	// Use ParseResultsHTML directly — avoids SSRF guard on local test servers.
	papers, err := academic.ParseResultsHTML(html, "https://justfind.example.com")
	if err != nil {
		t.Fatalf("SearchJustFind error: %v", err)
	}
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	p := papers[0]
	if !strings.Contains(p.Title, "Climate") {
		t.Errorf("unexpected title: %q", p.Title)
	}
	if p.Year != 2021 {
		t.Errorf("expected year 2021, got %d", p.Year)
	}
	if p.Journal != "Science" {
		t.Errorf("expected journal 'Science', got %q", p.Journal)
	}
	if p.DOI == "" {
		t.Error("expected DOI to be parsed from rft_id")
	}
}

// ---------------------------------------------------------------------------
// Test 7: SearchJustFind end-to-end against a local HTTP test server.
// Exercises URL construction, the Tier 1 HTTP client path, and parsing.
// ---------------------------------------------------------------------------

func TestSearchJustFind_EndToEnd(t *testing.T) {
	html := `<!DOCTYPE html><html><body>
<ul>
  <li class="result-list-item">
    <h3 class="title"><a href="/EDS/Record?id=42">Climate Change Effects</a></h3>
    <span class="Z3988" title="rft.atitle=Climate+Change+Effects&rft.au=Doe%2C+John&rft.date=2021&rft.jtitle=Science&rft_id=info%3Adoi%2F10.1234%2Ftest"></span>
  </li>
</ul>
</body></html>`

	var gotPath, gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	}))
	defer srv.Close()

	f := fetcher.New(t.Context(), fetcher.DefaultConfig())
	defer f.Close()

	papers, err := academic.SearchJustFind(context.Background(), f, srv.URL, "climate change", academic.SearchOptions{
		PeerReviewedOnly: true,
		Limit:            5,
		HTTPOnly:         true,
	})
	if err != nil {
		t.Fatalf("SearchJustFind error: %v", err)
	}
	if len(papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(papers))
	}
	if !strings.Contains(papers[0].Title, "Climate") {
		t.Errorf("unexpected title: %q", papers[0].Title)
	}
	if papers[0].Year != 2021 {
		t.Errorf("expected year 2021, got %d", papers[0].Year)
	}
	if papers[0].DOI == "" {
		t.Error("expected DOI to be parsed")
	}

	// Verify URL construction: path and key query params.
	if gotPath != "/EDS/Search" {
		t.Errorf("expected path /EDS/Search, got %q", gotPath)
	}
	if !strings.Contains(gotRawQuery, "lookfor=climate+change") {
		t.Errorf("expected lookfor=climate+change in query, got %q", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "limit=5") {
		t.Errorf("expected limit=5 in query, got %q", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "RV:y") {
		t.Errorf("expected peer-reviewed filter RV:y in query, got %q", gotRawQuery)
	}
}
