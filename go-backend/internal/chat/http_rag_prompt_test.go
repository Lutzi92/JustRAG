package chat_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

// Oracle: the prompts' fixed opening lines ("Du bist JustRAG" / "You are
// JustRAG"), and the chat send path's language fallback (an unsupported or
// missing language is answered in German, defaultLanguage) — the endpoint
// must show the prompt a turn with the same language input would get. The
// numbered rules must start their line in what is served (the source literal
// indents them).
func TestRAGSystemPrompt_LanguageAndDisplayForm(t *testing.T) {
	h := chat.NewHandler(&mockStore{}, nil, nil)
	for _, tc := range []struct{ query, lang, opening string }{
		{"?lang=de", "de", "Du bist JustRAG"},
		{"?lang=en", "en", "You are JustRAG"},
		{"", "de", "Du bist JustRAG"},
		{"?lang=fr", "de", "Du bist JustRAG"},
	} {
		rr := httptest.NewRecorder()
		h.RAGSystemPrompt(rr, httptest.NewRequest(http.MethodGet, "/api/chat/rag-system-prompt"+tc.query, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%q: expected 200, got %d", tc.query, rr.Code)
		}
		var body struct{ Language, Prompt string }
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Language != tc.lang || !strings.HasPrefix(body.Prompt, tc.opening) {
			t.Fatalf("%q: got lang %q, prompt starts %q", tc.query, body.Language, body.Prompt[:20])
		}
		if regexp.MustCompile(`(?m)^ +\d+[a-z]?\. `).MatchString(body.Prompt) {
			t.Fatalf("%q: a numbered rule keeps its source indentation", tc.query)
		}
		if !regexp.MustCompile(`(?m)^1\. `).MatchString(body.Prompt) {
			t.Fatalf("%q: rule 1 not at line start", tc.query)
		}
	}
}
