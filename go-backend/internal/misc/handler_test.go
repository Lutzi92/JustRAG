package misc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/misc"
)

// ---------------------------------------------------------------------------
// AI mock helpers (mirrors pattern from internal/ai/chat_test.go)
// ---------------------------------------------------------------------------

// buildChatServer starts an httptest server that serves /chat/completions.
func buildChatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// chatResponse builds a minimal OpenAI-compatible chat completion JSON body.
func chatResponse(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	}
}

// writeChatJSON encodes v as JSON to w with the appropriate content-type.
func writeChatJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Mock AI config store
// ---------------------------------------------------------------------------

var _ ai.ConfigStore = (*mockConfigStore)(nil)

type mockConfigStore struct {
	providerID string
	baseURL    string
	model      string
}

func (m *mockConfigStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{
		ID:      m.providerID,
		Name:    "Mock",
		APIKey:  "test-key",
		BaseURL: m.baseURL,
	}, nil
}

func (m *mockConfigStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (m *mockConfigStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{{Name: m.model}}, nil
}

func (m *mockConfigStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// resolverForServer creates a ConfigResolver backed by a test HTTP server.
func resolverForServer(t *testing.T, srvURL string) *ai.ConfigResolver {
	t.Helper()
	store := &mockConfigStore{
		providerID: "prov-test",
		baseURL:    srvURL,
		model:      "gpt-4o",
	}
	return ai.NewConfigResolver(store)
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

func withUser(r *http.Request) *http.Request {
	claims := &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Test 1: Enhance rewrite → 200 with enhanced query
// ---------------------------------------------------------------------------

func TestEnhance_Rewrite_OK(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("What is the boiling point of water?"))
	})

	resolver := resolverForServer(t, srv.URL)
	h := misc.NewHandler(resolver)

	body := map[string]any{
		"query":    "water boiling temp",
		"type":     "rewrite",
		"language": "en",
	}
	req := withUser(newRequest(http.MethodPost, "/api/enhance", body))
	rr := httptest.NewRecorder()
	h.Enhance(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["original"] != "water boiling temp" {
		t.Errorf("expected original=water boiling temp, got %q", resp["original"])
	}
	if resp["enhanced"] != "What is the boiling point of water?" {
		t.Errorf("expected enhanced from AI, got %q", resp["enhanced"])
	}
}

// ---------------------------------------------------------------------------
// Test 2: Enhance — missing auth → 401
// ---------------------------------------------------------------------------

func TestEnhance_NoAuth_401(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{"query": "test", "type": "rewrite"}
	req := newRequest(http.MethodPost, "/api/enhance", body)
	rr := httptest.NewRecorder()
	h.Enhance(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Enhance — unknown type → 400
// ---------------------------------------------------------------------------

func TestEnhance_InvalidType_400(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{"query": "test", "type": "summarize"}
	req := withUser(newRequest(http.MethodPost, "/api/enhance", body))
	rr := httptest.NewRecorder()
	h.Enhance(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Research BibTeX → text/plain with correct BibTeX entries
// ---------------------------------------------------------------------------

func TestResearchBibtex_OK(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{
		"findings": []map[string]any{
			{
				"sources": []map[string]any{
					{"fileId": "file-1", "fileName": "Annual Report 2023.pdf", "url": "https://example.com/report.pdf"},
					{"fileId": "file-2", "fileName": "Internal Memo.docx", "url": ""},
				},
			},
		},
	}
	req := withUser(newRequest(http.MethodPost, "/api/research/sess-1/bibtex", body))
	rr := httptest.NewRecorder()
	h.ResearchBibtex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", ct)
	}

	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "citations.bib") {
		t.Errorf("expected citations.bib in Content-Disposition, got %q", cd)
	}

	bib := rr.Body.String()
	if !strings.Contains(bib, "@online{") {
		t.Errorf("expected @online entry for source with URL, bib:\n%s", bib)
	}
	if !strings.Contains(bib, "@misc{") {
		t.Errorf("expected @misc entry for source without URL, bib:\n%s", bib)
	}
	if !strings.Contains(bib, "annual_report_2023") {
		t.Errorf("expected sanitized filename in key, bib:\n%s", bib)
	}
	if !strings.Contains(bib, "Accessed via JustRAG") {
		t.Errorf("expected 'Accessed via JustRAG' note, bib:\n%s", bib)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Research BibTeX deduplicates sources by FileID
// ---------------------------------------------------------------------------

func TestResearchBibtex_Deduplication(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{
		"findings": []map[string]any{
			{
				"sources": []map[string]any{
					{"fileId": "file-1", "fileName": "Doc.pdf", "url": ""},
				},
			},
			{
				"sources": []map[string]any{
					{"fileId": "file-1", "fileName": "Doc.pdf", "url": ""}, // duplicate
				},
			},
		},
	}
	req := withUser(newRequest(http.MethodPost, "/api/research/sess-1/bibtex", body))
	rr := httptest.NewRecorder()
	h.ResearchBibtex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	bib := rr.Body.String()
	count := strings.Count(bib, "@misc{")
	if count != 1 {
		t.Errorf("expected 1 deduplicated entry, got %d entries:\n%s", count, bib)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Academic BibTeX → @article for papers with journal
// ---------------------------------------------------------------------------

func TestAcademicBibtex_Article(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{
		"papers": []map[string]any{
			{
				"id":      "paper-1",
				"authors": []string{"Smith, John", "Doe, Jane"},
				"title":   "Advances in Machine Learning",
				"year":    2024,
				"journal": "Nature",
				"volume":  "10",
				"issue":   "2",
				"pages":   "100-120",
				"doi":     "10.1000/xyz",
				"url":     "https://example.com/paper",
			},
		},
	}
	req := withUser(newRequest(http.MethodPost, "/api/academic-research/sess-1/bibtex", body))
	rr := httptest.NewRecorder()
	h.AcademicBibtex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain, got %q", ct)
	}

	cd := rr.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "academic_citations.bib") {
		t.Errorf("expected academic_citations.bib in Content-Disposition, got %q", cd)
	}

	bib := rr.Body.String()
	if !strings.Contains(bib, "@article{") {
		t.Errorf("expected @article for paper with journal, bib:\n%s", bib)
	}
	if !strings.Contains(bib, "Smith, John and Doe, Jane") {
		t.Errorf("expected authors joined with ' and ', bib:\n%s", bib)
	}
	if !strings.Contains(bib, "Nature") {
		t.Errorf("expected journal field, bib:\n%s", bib)
	}
	if !strings.Contains(bib, "10.1000/xyz") {
		t.Errorf("expected doi field, bib:\n%s", bib)
	}
}

// ---------------------------------------------------------------------------
// Test 7: Academic BibTeX → @misc for papers without journal
// ---------------------------------------------------------------------------

func TestAcademicBibtex_NoJournal(t *testing.T) {
	h := misc.NewHandler(nil)

	body := map[string]any{
		"papers": []map[string]any{
			{
				"id":      "paper-2",
				"authors": []string{"Brown, Alice"},
				"title":   "Preprint on RAG Systems",
				"year":    2023,
			},
		},
	}
	req := withUser(newRequest(http.MethodPost, "/api/academic-research/sess-1/bibtex", body))
	rr := httptest.NewRecorder()
	h.AcademicBibtex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	bib := rr.Body.String()
	if !strings.Contains(bib, "@misc{") {
		t.Errorf("expected @misc for paper without journal, bib:\n%s", bib)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Academic BibTeX key disambiguation
// ---------------------------------------------------------------------------

func TestAcademicBibtex_KeyDisambiguation(t *testing.T) {
	h := misc.NewHandler(nil)

	// Two papers that would produce the same base key: smith2024advances
	body := map[string]any{
		"papers": []map[string]any{
			{
				"id":      "p1",
				"authors": []string{"Smith, John"},
				"title":   "Advances in AI",
				"year":    2024,
				"journal": "Journal A",
			},
			{
				"id":      "p2",
				"authors": []string{"Smith, Bob"},
				"title":   "Advances in ML",
				"year":    2024,
				"journal": "Journal B",
			},
		},
	}
	req := withUser(newRequest(http.MethodPost, "/api/academic-research/sess-1/bibtex", body))
	rr := httptest.NewRecorder()
	h.AcademicBibtex(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	bib := rr.Body.String()
	// Both papers map to "smith2024advances" → should get 'a' and 'b' suffixes.
	if !strings.Contains(bib, "smith2024advancesa") || !strings.Contains(bib, "smith2024advancesb") {
		t.Errorf("expected disambiguated keys smith2024advancesa/b, bib:\n%s", bib)
	}
}

// ---------------------------------------------------------------------------
// Test 9: LaTeX special character escaping
// ---------------------------------------------------------------------------

func TestGenerateResearchBibtex_LaTeXEscaping(t *testing.T) {
	findings := []misc.Finding{
		{Sources: []misc.Source{
			{FileID: "f1", FileName: "Report & Analysis 50% done.pdf", URL: ""},
		}},
	}
	bib := misc.GenerateResearchBibtex(findings)

	if !strings.Contains(bib, `\&`) {
		t.Errorf("expected \\& in output, got:\n%s", bib)
	}
	if !strings.Contains(bib, `\%`) {
		t.Errorf("expected \\%% in output, got:\n%s", bib)
	}
}

func TestGenerateAcademicBibtex_LaTeXEscaping(t *testing.T) {
	papers := []misc.AcademicPaper{
		{
			ID:      "p1",
			Authors: []string{"O'Brien & Co"},
			Title:   "Using $100 in Research",
			Year:    2024,
			Journal: "Proc. 100% Science",
		},
	}
	bib := misc.GenerateAcademicBibtex(papers)

	if !strings.Contains(bib, `\&`) {
		t.Errorf("expected \\& escaped in author, got:\n%s", bib)
	}
	if !strings.Contains(bib, `\$`) {
		t.Errorf("expected \\$ escaped in title, got:\n%s", bib)
	}
	if !strings.Contains(bib, `\%`) {
		t.Errorf("expected \\%% escaped in journal, got:\n%s", bib)
	}
}

// ---------------------------------------------------------------------------
// Test 10: GenerateResearchBibtex — pure unit test (no HTTP)
// ---------------------------------------------------------------------------

func TestGenerateResearchBibtex_OnlineEntry(t *testing.T) {
	findings := []misc.Finding{
		{Sources: []misc.Source{
			{FileID: "abc123", FileName: "My Report.pdf", URL: "https://example.com/doc"},
		}},
	}
	bib := misc.GenerateResearchBibtex(findings)

	if !strings.Contains(bib, "@online{") {
		t.Errorf("expected @online entry, got:\n%s", bib)
	}
	if !strings.Contains(bib, "url = {https://example.com/doc}") {
		t.Errorf("expected url field, got:\n%s", bib)
	}
	if !strings.Contains(bib, "urldate") {
		t.Errorf("expected urldate field, got:\n%s", bib)
	}
}

func TestGenerateResearchBibtex_MiscEntry(t *testing.T) {
	findings := []misc.Finding{
		{Sources: []misc.Source{
			{FileID: "xyz", FileName: "Confidential.pdf", URL: ""},
		}},
	}
	bib := misc.GenerateResearchBibtex(findings)

	if !strings.Contains(bib, "@misc{") {
		t.Errorf("expected @misc entry, got:\n%s", bib)
	}
	if strings.Contains(bib, "url") {
		t.Errorf("expected no url field for offline source, got:\n%s", bib)
	}
}
