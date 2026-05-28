// Package research — web.go provides the web-search branch used when
// NewAgent is run in "web" mode. It wires Google Custom Search to the
// shared Fetcher so each search result is fetched and extracted (Tier 1
// → Tier 2 browser fallback) before the content is handed to the
// synthesizer. Without this the agent would only ever see Google's short
// snippets, which is what the original TypeScript agent did.
package research

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/websearch"
)

// SiteConfigGetter is the minimal site-config interface the WebClient
// needs to read Google Custom Search credentials at run-time. Any store
// exposing GetSiteConfigValue (e.g. chat.PGStore) satisfies it.
type SiteConfigGetter interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// WebClient performs Google search → page fetch → readability extraction.
// Injected into the research Agent so the "web" mode has a real data
// source instead of falling back to KB search.
type WebClient struct {
	scg     SiteConfigGetter
	fetcher *fetcher.Fetcher
}

// NewWebClient returns a WebClient ready to search. Both params are
// required — pass nil for either to disable the web branch at the
// caller (NewAgent handles a nil WebClient gracefully).
func NewWebClient(scg SiteConfigGetter, f *fetcher.Fetcher) *WebClient {
	return &WebClient{scg: scg, fetcher: f}
}

// truncateUTF8 returns s truncated to at most maxBytes bytes without
// splitting a multi-byte UTF-8 rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// WebPage is a single fetched-and-extracted web page used as input to
// the synthesizer. Shaped so it slots into the same code path as KB
// search chunks (Content + source metadata).
type WebPage struct {
	Title   string
	URL     string
	Content string // extracted markdown
}

// Search runs a Google Custom Search for query and then fetches each
// hit via the shared Fetcher (ModeAuto — Tier 1, browser fallback on
// failure, readability extraction). Pages where extraction fails or
// returns very little content are silently skipped — better to return a
// shorter list than to synthesize from Google's 160-char snippets.
func (w *WebClient) Search(ctx context.Context, query string, limit int, lang string) ([]WebPage, error) {
	if w == nil || w.scg == nil || w.fetcher == nil {
		return nil, fmt.Errorf("web client not configured")
	}

	enabled, err := w.scg.GetSiteConfigValue(ctx, "web_search_enabled")
	if err != nil {
		return nil, fmt.Errorf("read web_search_enabled: %w", err)
	}
	if enabled == nil || *enabled != "true" {
		return nil, fmt.Errorf("web search is not enabled")
	}
	apiKey, err := w.scg.GetSiteConfigValue(ctx, "google_search_api_key")
	if err != nil {
		return nil, fmt.Errorf("failed to get google_search_api_key: %w", err)
	}
	if apiKey == nil || *apiKey == "" {
		return nil, fmt.Errorf("google_search_api_key is not configured")
	}
	cx, err := w.scg.GetSiteConfigValue(ctx, "google_search_cx")
	if err != nil {
		return nil, fmt.Errorf("failed to get google_search_cx: %w", err)
	}
	if cx == nil || *cx == "" {
		return nil, fmt.Errorf("google_search_cx is not configured")
	}

	if limit <= 0 || limit > 10 {
		limit = 5
	}
	if lang == "" {
		lang = "en"
	}

	hits, err := websearch.CallGoogle(ctx, *apiKey, *cx, query, limit, lang)
	if err != nil {
		return nil, fmt.Errorf("google search: %w", err)
	}

	pages := make([]WebPage, 0, len(hits))
	for _, h := range hits {
		// Check cancellation between pages — avoid hammering Chromium
		// when the client has already walked away.
		if ctx.Err() != nil {
			break
		}
		res, ferr := w.fetcher.Fetch(ctx, h.URL, fetcher.Options{Mode: fetcher.ModeAuto})
		if ferr != nil {
			// Failing one page must not fail the whole research step.
			continue
		}
		content := strings.TrimSpace(res.Markdown)
		if content == "" {
			content = strings.TrimSpace(res.HTML)
		}
		// Cap content so a single 200 KB page doesn't blow the LLM
		// context when we synthesize several hits at once. 5000 chars
		// matches the legacy TypeScript agent.
		content = truncateUTF8(content, 5000)
		if len(content) < 50 {
			// Extraction yielded nothing useful — skip rather than
			// pollute synthesis with an empty page.
			continue
		}
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = h.Title
		}
		pages = append(pages, WebPage{
			Title:   title,
			URL:     h.URL,
			Content: content,
		})
	}

	return pages, nil
}

// ---------------------------------------------------------------------------
// Agent — web-mode execution
// ---------------------------------------------------------------------------

// executeWebSearch is the "web" mode counterpart to executeSearch's
// default KB path. It runs a Google search, fetches each result via
// the Fetcher, and synthesizes findings from the extracted page
// content. On any step failure the PlanStep is marked failed/complete
// so the outer loop can continue — one dead URL must not sink the run.
func (a *Agent) executeWebSearch(ctx context.Context, step *PlanStep) {
	pages, err := a.webClient.Search(ctx, step.Query, a.opts.ChunksPerSearch, a.opts.Language)
	if err != nil {
		step.Status = "failed"
		step.Result = fmt.Sprintf("web search failed: %s", err.Error())
		return
	}
	if len(pages) == 0 {
		step.Status = "failed"
		step.Result = "no usable web results"
		return
	}

	step.DocumentsProcessed = len(pages)

	a.emit(ProgressEvent{
		Type:    "web_fetching",
		Message: fmt.Sprintf("Fetched %d web pages for: %s", len(pages), step.Query),
	})

	finding, err := a.synthesizeWeb(ctx, step.Query, pages)
	if err != nil || finding == nil {
		step.Status = "complete"
		return
	}

	if !a.isDuplicate(finding) {
		a.state.Findings = append(a.state.Findings, *finding)
		a.emit(ProgressEvent{
			Type:     "finding",
			Message:  "New finding discovered",
			Findings: []Finding{*finding},
		})
	}

	step.Status = "complete"
	step.Result = finding.Content
}

// synthesizeWeb mirrors Agent.synthesize but takes WebPage inputs and
// records URL-based sources instead of file IDs. Prompt shape is kept
// identical so the synthesizer LLM sees the same instruction template.
func (a *Agent) synthesizeWeb(ctx context.Context, query string, pages []WebPage) (*Finding, error) {
	if len(pages) == 0 {
		return nil, nil
	}

	a.emit(ProgressEvent{
		Type:    "synthesizing",
		Message: fmt.Sprintf("Synthesizing %d web pages for: %s", len(pages), query),
	})

	var sb strings.Builder
	sources := make([]SourceRef, 0, len(pages))
	for i, p := range pages {
		fmt.Fprintf(&sb, "[Page %d | %s]\n%s\n\n", i+1, p.Title, p.Content)
		sources = append(sources, SourceRef{
			FileName:     p.Title,
			URL:          p.URL,
			ChunkContent: truncate(p.Content, 200),
		})
	}

	prompt := fmt.Sprintf(
		"Extract key facts from the following web pages that answer this query: %s\n\nPages:\n%s\n"+
			`Return JSON only: {"facts": ["fact1", "fact2"], "needsMoreInfo": false}`,
		query, sb.String(),
	)
	system := "You are a research assistant. Extract factual information concisely. Respond with valid JSON only."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		return nil, err
	}

	var parsed struct {
		Facts         []string `json:"facts"`
		NeedsMoreInfo bool     `json:"needsMoreInfo"`
	}
	if err := parseJSON(result.Content, &parsed); err != nil || len(parsed.Facts) == 0 {
		return nil, nil
	}

	return &Finding{
		Content:        strings.Join(parsed.Facts, " "),
		Sources:        sources,
		RelevanceScore: 0.8,
	}, nil
}
