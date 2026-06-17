package contentgen_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/contentgen"
	"github.com/justrag/go-backend/internal/gencontent"
)

// TestTextArtifacts_Registry asserts the registry exposes the expected text
// artifacts, each fully populated. New text artifacts should extend this list.
func TestTextArtifacts_Registry(t *testing.T) {
	want := map[string]bool{
		"briefing_doc": false,
		"faq":          false,
		"study_guide":  false,
		"timeline":     false,
	}
	for _, spec := range contentgen.TextArtifacts {
		if _, ok := want[spec.Type]; ok {
			want[spec.Type] = true
		}
		if spec.TitleFn == nil {
			t.Errorf("%s: TitleFn is nil", spec.Type)
		}
		if spec.SystemPrompt == nil {
			t.Errorf("%s: SystemPrompt is nil", spec.Type)
		}
		if spec.TitleFn != nil && spec.TitleFn("X") == "" {
			t.Errorf("%s: TitleFn returned empty title", spec.Type)
		}
		if spec.SystemPrompt != nil && spec.SystemPrompt("en") == "" {
			t.Errorf("%s: SystemPrompt(en) returned empty", spec.Type)
		}
	}
	for typ, found := range want {
		if !found {
			t.Errorf("expected text artifact %q in registry", typ)
		}
	}
}

// TestGenerateArtifact_Returns200 exercises the generic handler end-to-end and
// confirms it stores the {text: markdown} shape under the artifact's type.
func TestGenerateArtifact_Returns200(t *testing.T) {
	llmContent := "## Key Points\n\n- A\n- B"
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	for _, spec := range contentgen.TextArtifacts {
		t.Run(spec.Type, func(t *testing.T) {
			req := postJSON(t, "/api/kb/"+testKBID+"/generate/"+spec.Type, map[string]string{"topic": "test topic"})
			rec := httptest.NewRecorder()
			h.GenerateArtifact(spec)(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
			}

			var resp gencontent.GenContentRow
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if resp.Type != spec.Type {
				t.Errorf("expected type %s, got %s", spec.Type, resp.Type)
			}
			content, ok := resp.Content.(map[string]any)
			if !ok {
				t.Fatalf("expected map content, got %T", resp.Content)
			}
			if content["text"] != llmContent {
				t.Errorf("expected stored text %q, got %q", llmContent, content["text"])
			}
		})
	}
}

func TestGenerateQuiz_Returns200(t *testing.T) {
	llmContent := `[{"question":"What is X?","options":["A","B","C","D"],"answerIndex":1,"explanation":"Because B."}]`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/quiz", map[string]string{"topic": "test topic"})
	rec := httptest.NewRecorder()
	h.GenerateQuiz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "quiz" {
		t.Errorf("expected type quiz, got %s", resp.Type)
	}
	arr, ok := resp.Content.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected 1-element array content, got %T", resp.Content)
	}
}

func TestGenerateQuiz_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/quiz", map[string]string{"topic": ""})
	rec := httptest.NewRecorder()
	h.GenerateQuiz(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGenerateArtifact_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)
	spec := contentgen.TextArtifacts[0]

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/"+spec.Type, map[string]string{"topic": ""})
	rec := httptest.NewRecorder()
	h.GenerateArtifact(spec)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGenerateArtifact_NoAuth_Returns401(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)
	spec := contentgen.TextArtifacts[0]

	body, _ := json.Marshal(map[string]string{"topic": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/kb/"+testKBID+"/generate/"+spec.Type, bytes.NewReader(body))
	req.SetPathValue("id", testKBID)
	// no auth injected
	rec := httptest.NewRecorder()
	h.GenerateArtifact(spec)(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
