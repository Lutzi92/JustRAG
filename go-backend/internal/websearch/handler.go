// Package websearch provides the POST /api/kb/{id}/websearch handler.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
	"github.com/justrag/go-backend/internal/httputil"
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// WebSearchStore is the persistence interface required by Handler.
type WebSearchStore interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the web search endpoint.
type Handler struct {
	store WebSearchStore
}

// NewHandler creates a Handler backed by store.
func NewHandler(store WebSearchStore) *Handler {
	return &Handler{store: store}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Request / Response shapes
// ---------------------------------------------------------------------------

type webSearchRequest struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	Language string `json:"language"`
}

type searchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

// Hit is the exported shape of a single Google Custom Search result.
// Used by other packages (e.g. research agent) that reuse CallGoogle.
type Hit struct {
	Title   string
	Snippet string
	URL     string
}

// CallGoogle performs a Google Custom Search API call and returns the
// parsed hits. This is the exported entry point other packages use to
// reuse the shared search path.
func CallGoogle(ctx context.Context, apiKey, cx, query string, limit int, lang string) ([]Hit, error) {
	results, err := callGoogleSearch(ctx, apiKey, cx, query, limit, lang)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, len(results))
	for i, r := range results {
		out[i] = Hit(r)
	}
	return out, nil
}

type webSearchResponse struct {
	Results []searchResult `json:"results"`
}

// ---------------------------------------------------------------------------
// Google Custom Search API response types (partial)
// ---------------------------------------------------------------------------

type googleSearchResponse struct {
	Items []googleSearchItem `json:"items"`
}

type googleSearchItem struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Link    string `json:"link"`
}

// ---------------------------------------------------------------------------
// WebSearch handles POST /api/kb/{id}/websearch.
// ---------------------------------------------------------------------------

// WebSearch handles POST /api/kb/{id}/websearch.
// Requires KB view permission (enforced by kbaccess middleware).
// Accepts {"query": "...", "limit": 5, "language": "de"} and returns
// results from the Google Custom Search API.
func (h *Handler) WebSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Parse request body.
	var req webSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "query is required")
		return
	}

	// Apply defaults.
	if req.Limit <= 0 || req.Limit > 10 {
		req.Limit = 5
	}
	if req.Language == "" {
		req.Language = "en"
	}

	// 2. Check web_search_enabled site config.
	enabledVal, err := h.store.GetSiteConfigValue(ctx, "web_search_enabled")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to read site config")
		return
	}
	if enabledVal == nil || *enabledVal != "true" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "web search is not enabled")
		return
	}

	// 3. Get Google API credentials.
	apiKeyVal, err := h.store.GetSiteConfigValue(ctx, "google_search_api_key")
	if err != nil || apiKeyVal == nil || *apiKeyVal == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Google Search API key is not configured")
		return
	}
	cxVal, err := h.store.GetSiteConfigValue(ctx, "google_search_cx")
	if err != nil || cxVal == nil || *cxVal == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Google Search CX is not configured")
		return
	}

	// 4. Call Google Custom Search API.
	results, err := callGoogleSearch(ctx, *apiKeyVal, *cxVal, req.Query, req.Limit, req.Language)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadGateway, fmt.Sprintf("Google Search API error: %s", httputil.SanitizeError(err)))
		return
	}

	// 5. Return results.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, webSearchResponse{Results: results})
}

// googleSearchEndpoint is the Google Custom Search JSON API URL. Tests
// override this to point at an httptest.Server.
var googleSearchEndpoint = "https://www.googleapis.com/customsearch/v1"

// callGoogleSearch calls the Google Custom Search JSON API and returns parsed results.
func callGoogleSearch(ctx context.Context, apiKey, cx, query string, limit int, lang string) ([]searchResult, error) {
	endpoint := googleSearchEndpoint

	params := url.Values{}
	params.Set("key", apiKey)
	params.Set("cx", cx)
	params.Set("q", query)
	params.Set("num", strconv.Itoa(limit))
	params.Set("lr", "lang_"+lang)

	reqURL := endpoint + "?" + params.Encode()

	httpClient := httpclient.New(15 * time.Second)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call Google API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Google API returned status %d: %s", resp.StatusCode, string(body))
	}

	var googleResp googleSearchResponse
	if err := json.Unmarshal(body, &googleResp); err != nil {
		return nil, fmt.Errorf("parse Google API response: %w", err)
	}

	results := make([]searchResult, 0, len(googleResp.Items))
	for _, item := range googleResp.Items {
		results = append(results, searchResult{
			Title:   item.Title,
			Snippet: item.Snippet,
			URL:     item.Link,
		})
	}
	return results, nil
}
