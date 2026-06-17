package contentgen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/prompts"
)

// ArtifactSpec describes a markdown-text generated-content artifact (briefing
// doc, FAQ, study guide, timeline, ...). Every such artifact follows the same
// retrieve→generate→store flow as GenerateAnalysis; only the system prompt,
// stored content type, title, and retrieval breadth differ. New text artifacts
// are added by appending an ArtifactSpec to TextArtifacts plus one prompt
// function in the prompts package — no new handler or route boilerplate, and
// no migration (generated_content.type is a free string, content is JSONB).
type ArtifactSpec struct {
	// Type is both the stored content type and the URL segment
	// (POST /api/kb/{id}/generate/{Type}). Keep it snake_case and stable;
	// the frontend keys rendering off it.
	Type string
	// TitleFn builds the stored title from the requested topic.
	TitleFn func(topic string) string
	// SystemPrompt returns the system prompt for the given UI language.
	SystemPrompt func(lang string) string
	// ContextLimit caps how many chunks feed the prompt.
	// Zero ⇒ defaultArtifactContextLimit.
	ContextLimit int
}

const defaultArtifactContextLimit = 25

// TextArtifacts is the registry of markdown-text artifacts served by the
// generic GenerateArtifact handler. Order is irrelevant; routes are registered
// by iterating this slice. Output is stored as {"text": markdown}, the same
// content shape as analysis, so the frontend renders, edits, and exports these
// through its existing markdown-artifact path.
var TextArtifacts = []ArtifactSpec{
	{
		Type:         "briefing_doc",
		TitleFn:      func(topic string) string { return "Briefing: " + topic },
		SystemPrompt: prompts.BriefingDocSystemPrompt,
		ContextLimit: 40,
	},
	{
		Type:         "faq",
		TitleFn:      func(topic string) string { return "FAQ: " + topic },
		SystemPrompt: prompts.FAQSystemPrompt,
	},
	{
		Type:         "study_guide",
		TitleFn:      func(topic string) string { return "Study Guide: " + topic },
		SystemPrompt: prompts.StudyGuideSystemPrompt,
		ContextLimit: 40,
	},
	{
		Type:         "timeline",
		TitleFn:      func(topic string) string { return "Timeline: " + topic },
		SystemPrompt: prompts.TimelineSystemPrompt,
		ContextLimit: 40,
	},
}

// GenerateArtifact returns an http.HandlerFunc that retrieves KB context for the
// requested topic, generates a markdown document with the artifact's system
// prompt (using the KB chat model, like analysis), and stores it as
// {"text": markdown}. Runs inline — a single LLM call, no worker needed.
func (h *Handler) GenerateArtifact(spec ArtifactSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		kbID := kbIDFromContext(r)

		caller := auth.UserFromContext(ctx)
		if caller == nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
			return
		}

		var req GenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if strings.TrimSpace(req.Topic) == "" {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "topic is required")
			return
		}
		lang := defaultLang(req.Language)

		limit := spec.ContextLimit
		if limit <= 0 {
			limit = defaultArtifactContextLimit
		}
		contextStr, err := getContext(ctx, h.searchSvc, kbID, req.Topic, limit)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to retrieve context")
			return
		}

		prompt := fmt.Sprintf("Topic: %s\n\nContext:\n%s", req.Topic, contextStr)
		text, err := generateWithLLM(ctx, h.aiResolver, prompt, spec.SystemPrompt(lang), kbID)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "LLM generation failed")
			return
		}

		// Same content shape as GenerateAnalysis ({"text": markdown}) so the
		// frontend's markdown-artifact path renders/edits/exports it unchanged.
		contentVal := map[string]any{"text": strings.TrimSpace(text)}
		record, err := h.store.CreateGeneratedContent(ctx, kbID, caller.ID, spec.Type, spec.TitleFn(req.Topic), contentVal)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to save generated content")
			return
		}

		httputil.WriteJSONCtx(ctx, w, http.StatusOK, record)
	}
}
