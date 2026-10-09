package chat

import (
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/prompts"
)

// ragSystemPromptResponse is the body of GET /api/chat/rag-system-prompt.
type ragSystemPromptResponse struct {
	Language string `json:"language"`
	Prompt   string `json:"prompt"`
}

// RAGSystemPrompt handles GET /api/chat/rag-system-prompt?lang=de|en. It
// returns, read-only, the fixed answer instructions of the default answer
// path (prompts.ChatSystemPromptDisplay — prompts.ChatSystemPrompt without
// its source indentation), which that path appends after the KB's own system
// prompt; the system prompt panel shows it so KB owners see what their
// prompt is combined with.
//
// It is not the complete prompt of a turn. Per turn the default path adds
// the current-date line, the low-confidence or abstain notice, the
// enumeration/recency/conflict addenda and the CONTEXT block; the session
// and long-term memory blocks are prepended to the KB prompt and the tabular
// guidance appended to it, when those features are on. The corpus-table path
// and the document-comparison summary without a team use their own
// instructions instead of these.
//
// The language comes from ?lang alone, with the same fallback a chat turn
// applies to an unsupported language (defaultLanguage), not from KB or user
// settings.
func (h *Handler) RAGSystemPrompt(w http.ResponseWriter, r *http.Request) {
	lang := requestLanguage(r.URL.Query().Get("lang"))
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ragSystemPromptResponse{
		Language: lang,
		Prompt:   prompts.ChatSystemPromptDisplay(lang),
	})
}

// requestLanguage maps a ?lang value to a supported prompt language, falling
// back like POST /api/kb/{id}/chat does for an unsupported body.language.
func requestLanguage(v string) string {
	if supportedLanguages[v] {
		return v
	}
	return defaultLanguage
}
