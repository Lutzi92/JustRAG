package contentgen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/contentgen"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	testKBID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testUserID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ contentgen.Store         = (*mockStore)(nil)
	_ contentgen.Searcher      = (*mockSearcher)(nil)
	_ ai.ConfigStore           = (*mockConfigStore)(nil)
	_ contentgen.AsynqEnqueuer = (*mockAsynqClient)(nil)
)

type mockStore struct {
	created *gencontent.GenContentRow
	err     error
}

func (m *mockStore) CreateGeneratedContent(_ context.Context, kbID, userID, contentType, title string, content any) (*gencontent.GenContentRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.created != nil {
		return m.created, nil
	}
	return &gencontent.GenContentRow{
		ID:        "cccccccc-cccc-cccc-cccc-cccccccccccc",
		KbID:      kbID,
		UserID:    userID,
		Type:      contentType,
		Title:     title,
		Content:   content,
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// ---------------------------------------------------------------------------
// Mock searcher
// ---------------------------------------------------------------------------

type mockSearcher struct {
	chunks []vector.SearchChunk
	err    error
}

func (m *mockSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &vector.SearchResult{Chunks: m.chunks}, nil
}

// defaultSearcher returns a mock searcher with one sample chunk.
func defaultSearcher() *mockSearcher {
	return &mockSearcher{
		chunks: []vector.SearchChunk{
			{
				ID:       "chunk-1",
				Content:  "This is test content about the topic.",
				FileID:   "file-1",
				FileName: "test.pdf",
				Score:    0.9,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Mock AI config store (for building a ConfigResolver)
// ---------------------------------------------------------------------------

type mockConfigStore struct {
	baseURL string
	model   string
}

func (m *mockConfigStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{
		ID:      "prov-test",
		Name:    "Test",
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

// ---------------------------------------------------------------------------
// LLM test server helpers
// ---------------------------------------------------------------------------

// buildLLMServer creates an httptest server that returns content as a chat
// completion response. It handles POST /chat/completions.
func buildLLMServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// resolverFor creates a ConfigResolver pointing at the given test server.
func resolverFor(srv *httptest.Server) *ai.ConfigResolver {
	cs := &mockConfigStore{baseURL: srv.URL, model: "gpt-test"}
	return ai.NewConfigResolver(cs)
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

func withAuth(r *http.Request) *http.Request {
	claims := &auth.Claims{ID: testUserID, Role: "user"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func postJSON(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.SetPathValue("id", testKBID)
	return withAuth(req)
}

// ---------------------------------------------------------------------------
// Tests: GenerateCards
// ---------------------------------------------------------------------------

func TestGenerateCards_Returns200(t *testing.T) {
	llmContent := `[{"front": "What is X?", "back": "X is Y."}]`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/cards", map[string]string{"topic": "test topic"})
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "flashcards" {
		t.Errorf("expected type flashcards, got %s", resp.Type)
	}
}

func TestGenerateCards_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/cards", map[string]string{"topic": ""})
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGenerateCards_NoAuth_Returns401(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"topic": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/kb/"+testKBID+"/generate/cards", bytes.NewReader(body))
	req.SetPathValue("id", testKBID)
	// no auth injected
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateAnalysis
// ---------------------------------------------------------------------------

// generateAnalysisResponse mirrors GenerateAnalysis's response envelope
// ({record, degradedReason}) — it stopped returning the record directly once
// GenerateAnalysis could run through an agent/team (Task 11).
type generateAnalysisResponse struct {
	Record         gencontent.GenContentRow `json:"record"`
	DegradedReason string                   `json:"degradedReason"`
}

func TestGenerateAnalysis_Returns200(t *testing.T) {
	llmContent := "## Analysis\n\nThis is a detailed analysis."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis", map[string]string{"topic": "test analysis"})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp generateAnalysisResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Record.Type != "analysis" {
		t.Errorf("expected type analysis, got %s", resp.Record.Type)
	}
	if resp.DegradedReason != "" {
		t.Errorf("degradedReason = %q, want \"\" (no selection made, h.teams is nil)", resp.DegradedReason)
	}
}

// mockTeamLoader is a minimal contentgen.TeamLoader for the handler-level
// tests below. Defined here (package contentgen_test) rather than reusing
// analysis_agent_test.go's unexported fakeTeamLoader, which lives in the
// internal contentgen package and isn't visible from this external test
// package.
type mockTeamLoader struct {
	agent *agentteams.AgentRecord
	team  *agentteams.TeamForChat
	err   error
}

func (m mockTeamLoader) LoadAgentForChat(context.Context, string, string) (*agentteams.AgentRecord, error) {
	return m.agent, m.err
}
func (m mockTeamLoader) LoadTeamForChat(context.Context, string, string) (*agentteams.TeamForChat, error) {
	return m.team, m.err
}

// mockTeamSearcher implements vector.Searcher (Search + ExpandNeighbors —
// contentgen.Searcher only requires Search, which is why SetAgentDeps takes
// a second, separately typed field; see http.go). called is set on every
// Search invocation so tests can assert whether the agent/team path (which
// alone uses this searcher, via RunTeamChat) actually ran.
type mockTeamSearcher struct {
	called *bool
	chunks []vector.SearchChunk
}

func (m mockTeamSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	if m.called != nil {
		*m.called = true
	}
	return &vector.SearchResult{Chunks: m.chunks}, nil
}

func (m mockTeamSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

var _ vector.Searcher = mockTeamSearcher{}

// Positive guard: a resolvable agentId must actually run the team/agent
// pipeline (RunTeamChat), not just resolve without error. Detected via the
// team searcher being invoked — only the agent path uses h.teamSearcher, the
// standard path uses h.searchSvc.
func TestGenerateAnalysisWithAgentRunsTeamChat(t *testing.T) {
	llmContent := "## Analysis\n\nSynthesized by the agent."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	var called bool
	searcher := mockTeamSearcher{
		called: &called,
		chunks: []vector.SearchChunk{
			{ID: "c1", Content: "evidence", FileID: "f1", FileName: "f.pdf", Score: 0.8},
		},
	}
	h.SetAgentDeps(mockTeamLoader{agent: &agentteams.AgentRecord{ID: "a1", Name: "Prüfer", ChatModel: "gpt-test"}}, searcher)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis",
		map[string]string{"topic": "Budget 2026", "agentId": "a1"})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp generateAnalysisResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.DegradedReason != "" {
		t.Errorf("degradedReason = %q, want \"\" (agent resolved and ran)", resp.DegradedReason)
	}
	if !called {
		t.Error("with agentId, RunTeamChat is called — the team searcher was never invoked, so the agent path did not run")
	}
}

// Fail-soft-Wächter: eine im Dialog getroffene, inzwischen ungültige Auswahl
// kostet den Lauf nicht — sie MUSS aber sichtbar werden.
func TestGenerateAnalysisWithUnresolvableAgentDegrades(t *testing.T) {
	llmContent := "## Analysis\n\nThis is a detailed analysis."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)
	h.SetAgentDeps(mockTeamLoader{err: errors.New("not attached")}, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis",
		map[string]string{"topic": "test analysis", "agentId": "a1"})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-soft): %s", rec.Code, rec.Body.String())
	}
	var resp generateAnalysisResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Record.Type != "analysis" {
		t.Errorf("trotz Degradation muss ein Artefakt entstehen, got type %q", resp.Record.Type)
	}
	if resp.DegradedReason == "" {
		t.Error("degradedReason ist leer — der Lauf degradiert stumm, genau das soll ausgeschlossen sein")
	}
}

func TestGenerateAnalysis_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis", map[string]string{})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateAbstract
// ---------------------------------------------------------------------------

func TestGenerateAbstract_Returns200(t *testing.T) {
	llmContent := "Background: ... Methods: ... Results: ... Conclusions: ..."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/abstract", map[string]string{
		"fileId":       "file-1",
		"abstractType": "academic",
		"language":     "en",
	})
	rec := httptest.NewRecorder()
	h.GenerateAbstract(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "abstract" {
		t.Errorf("expected type abstract, got %s", resp.Type)
	}
}

func TestGenerateAbstract_MissingFileID_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/abstract", map[string]string{
		"abstractType": "academic",
	})
	rec := httptest.NewRecorder()
	h.GenerateAbstract(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateChart
// ---------------------------------------------------------------------------

// With no chart deps wired, GenerateChart uses the LLM-from-context path and
// returns a parsed chart spec.
func TestGenerateChart_LLMPath_Returns200(t *testing.T) {
	llmContent := `{"description":"Sales by region","type":"bar","config":{"xAxis":"region","keys":["sales"]},"series":[{"region":"North","sales":120},{"region":"South","sales":90}]}`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/chart", map[string]string{"topic": "sales by region"})
	rec := httptest.NewRecorder()
	h.GenerateChart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "chart" {
		t.Errorf("expected type chart, got %s", resp.Type)
	}
	content, ok := resp.Content.(map[string]any)
	if !ok {
		t.Fatalf("expected map content, got %T", resp.Content)
	}
	if content["type"] != "bar" {
		t.Errorf("expected chart type bar, got %v", content["type"])
	}
	series, ok := content["series"].([]any)
	if !ok || len(series) != 2 {
		t.Errorf("expected 2 series points, got %v", content["series"])
	}
}

// An LLM response missing required fields is rejected (no partial chart saved).
func TestGenerateChart_IncompleteSpec_Returns500(t *testing.T) {
	srv := buildLLMServer(t, `{"type":"bar"}`)
	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/chart", map[string]string{"topic": "x"})
	rec := httptest.NewRecorder()
	h.GenerateChart(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestGenerateChart_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/chart", map[string]string{"topic": ""})
	rec := httptest.NewRecorder()
	h.GenerateChart(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GeneratePresentation
// ---------------------------------------------------------------------------

func TestGeneratePresentation_Returns200(t *testing.T) {
	llmContent := `{"title":"Test Presentation","slides":[{"title":"Slide 1","content":["Point A"],"speakerNotes":"..."}]}`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/presentation", map[string]string{"topic": "test pres"})
	rec := httptest.NewRecorder()
	h.GeneratePresentation(rec, req)

	// Clean up generated PPTX file (written under data/)
	t.Cleanup(func() { os.RemoveAll("data") })

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "presentation" {
		t.Errorf("expected type presentation, got %s", resp.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: GeneratePodcast
// ---------------------------------------------------------------------------

// mockAsynqClient satisfies contentgen.AsynqEnqueuer for tests.
type mockAsynqClient struct {
	lastPayload []byte
}

func (m *mockAsynqClient) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	m.lastPayload = task.Payload()
	return &asynq.TaskInfo{ID: "test-job-id"}, nil
}

func TestGeneratePodcast_Returns202(t *testing.T) {
	ac := &mockAsynqClient{}
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), ac, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/podcast", map[string]string{"topic": "test podcast"})
	rec := httptest.NewRecorder()
	h.GeneratePodcast(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["jobId"] == "" {
		t.Error("expected non-empty jobId")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetWorkspacePresets
// ---------------------------------------------------------------------------

func TestGetWorkspacePresetsFallsBackToDefaults(t *testing.T) {
	h := contentgen.NewHandler(nil, nil, nil, nil, nil, nil) // ohne SetPresetDeps
	req := httptest.NewRequest(http.MethodGet, "/api/kb/"+testKBID+"/workspace/presets?lang=de", nil)
	req.SetPathValue("id", testKBID)
	rec := httptest.NewRecorder()
	h.GetWorkspacePresets(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Analysis       []contentgen.Preset `json:"analysis"`
		Comparison     []contentgen.Preset `json:"comparison"`
		CompareEnabled bool                `json:"compareEnabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Analysis) != len(contentgen.DefaultAnalysisPresets("de")) {
		t.Errorf("analysis = %d presets, want the %d defaults", len(body.Analysis), len(contentgen.DefaultAnalysisPresets("de")))
	}
	// DE and EN default lists happen to have the same length (4 and 3), so a
	// length-only check would not catch ?lang=de being resolved as English
	// (or vice versa). Compare an actual label too.
	if len(body.Analysis) == 0 || body.Analysis[0].Label != contentgen.DefaultAnalysisPresets("de")[0].Label {
		t.Errorf("analysis[0].Label = %q, want the DE default %q", firstLabel(body.Analysis), contentgen.DefaultAnalysisPresets("de")[0].Label)
	}
	if len(body.Comparison) == 0 {
		t.Error("comparison presets must not be empty")
	}
	if body.CompareEnabled {
		t.Error("compareEnabled must be false when no reader is wired")
	}
}

func firstLabel(presets []contentgen.Preset) string {
	if len(presets) == 0 {
		return "<empty>"
	}
	return presets[0].Label
}

// ---------------------------------------------------------------------------
// Tests: SetPresetDeps / readerForKB overlay wiring
// ---------------------------------------------------------------------------
//
// TestGetWorkspacePresetsFallsBackToDefaults above only ever exercises the
// handler with SetPresetDeps unset — it proves the code-default fallback,
// not that a KB override actually reaches the response. The tests below are
// what "mit KB-Override" in the commit message needs: a fake global reader
// AND a fake per-KB override lister wired together via SetPresetDeps, so the
// KBOverlayReader construction in readerForKB is on the exercised path.

// fakeGlobalReader satisfies contentgen.SiteConfigReader for these tests.
type fakeGlobalReader struct{ vals map[string]string }

func (f fakeGlobalReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

// fakeKBOverrides satisfies contentgen.KBConfigOverrideLister for these
// tests. err, when set, simulates a failed per-KB override load.
type fakeKBOverrides struct {
	overrides map[string]*string
	err       error
}

func (f fakeKBOverrides) ListKBOverrides(_ context.Context, _ string) (map[string]*string, error) {
	return f.overrides, f.err
}

func strPtr(s string) *string { return &s }

func getWorkspacePresetsAnalysisLabel(t *testing.T, h *contentgen.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/kb/"+testKBID+"/workspace/presets?lang=de", nil)
	req.SetPathValue("id", testKBID)
	rec := httptest.NewRecorder()
	h.GetWorkspacePresets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Analysis []contentgen.Preset `json:"analysis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Analysis) == 0 {
		t.Fatal("analysis presets must not be empty")
	}
	return body.Analysis[0].Label
}

func TestGetWorkspacePresetsUsesKBOverrideOverGlobal(t *testing.T) {
	global := fakeGlobalReader{vals: map[string]string{
		"workspace_analysis_presets": `[{"label":"Global","prompt":"G"}]`,
	}}
	kbOverrides := fakeKBOverrides{overrides: map[string]*string{
		"workspace_analysis_presets": strPtr(`[{"label":"Override","prompt":"O"}]`),
	}}
	h := contentgen.NewHandler(nil, nil, nil, nil, nil, nil)
	h.SetPresetDeps(global, kbOverrides)

	got := getWorkspacePresetsAnalysisLabel(t, h)
	if got != "Override" {
		t.Fatalf("analysis[0].Label = %q, want the KB override %q (not the global value or a code default)", got, "Override")
	}
}

func TestGetWorkspacePresetsFallsBackToGlobalWhenKBHasNoOverride(t *testing.T) {
	global := fakeGlobalReader{vals: map[string]string{
		"workspace_analysis_presets": `[{"label":"Global","prompt":"G"}]`,
	}}
	kbOverrides := fakeKBOverrides{overrides: map[string]*string{}} // KB set nothing
	h := contentgen.NewHandler(nil, nil, nil, nil, nil, nil)
	h.SetPresetDeps(global, kbOverrides)

	got := getWorkspacePresetsAnalysisLabel(t, h)
	if got != "Global" {
		t.Fatalf("analysis[0].Label = %q, want the global value %q", got, "Global")
	}
}

func TestGetWorkspacePresetsDegradesToGlobalWhenKBOverrideLoadFails(t *testing.T) {
	global := fakeGlobalReader{vals: map[string]string{
		"workspace_analysis_presets": `[{"label":"Global","prompt":"G"}]`,
	}}
	kbOverrides := fakeKBOverrides{err: errors.New("db unreachable")}
	h := contentgen.NewHandler(nil, nil, nil, nil, nil, nil)
	h.SetPresetDeps(global, kbOverrides)

	got := getWorkspacePresetsAnalysisLabel(t, h)
	if got != "Global" {
		t.Fatalf("analysis[0].Label = %q, want the global value %q (degrade on load failure)", got, "Global")
	}
}
