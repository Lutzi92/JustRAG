package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
)

// ErrConfluenceAuthFailed indicates the Confluence API rejected the token
// (HTTP 401 or 403). Wrapped into request errors so callers can detect this
// with errors.Is and take auth-recovery actions — e.g. marking the stored
// connection as 'error' so the Profile UI reflects reality.
var ErrConfluenceAuthFailed = errors.New("confluence authentication failed")

// ConfluenceClient is a thin REST client for the Confluence Server/Data Center API.
type ConfluenceClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewConfluenceClient creates a ConfluenceClient that authenticates with a
// personal-access token (Bearer auth).
func NewConfluenceClient(baseURL, token string) *ConfluenceClient {
	return &ConfluenceClient{
		baseURL: baseURL,
		token:   token,
		http:    httpclient.New(30 * time.Second),
	}
}

// get executes a GET request against the Confluence REST API and returns the
// decoded JSON body as a map.  The caller is responsible for further type
// assertions.
func (c *ConfluenceClient) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("confluence returned status %d: %w", resp.StatusCode, ErrConfluenceAuthFailed)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("confluence returned status %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// VerifyConnection calls GET /rest/api/user/current and returns an error if the
// token is invalid or the request fails.
func (c *ConfluenceClient) VerifyConnection(ctx context.Context) error {
	_, err := c.get(ctx, "/rest/api/user/current")
	return err
}

// LookupUser resolves a ri:userkey (Data Center/Server) or ri:account-id
// (Cloud) to its display name via /rest/api/user. The endpoint accepts
// whichever param matches the deployment; we try key= first (DC) and fall
// back to accountId= if the key lookup fails.
//
// Callers wanting to batch or cache should do so at the caller level —
// this is a single HTTP roundtrip per call. See resolveUserMentions for
// the per-sync cache that sits on top.
func (c *ConfluenceClient) LookupUser(ctx context.Context, key string) (string, error) {
	q := url.Values{}
	q.Set("key", key)
	body, err := c.get(ctx, "/rest/api/user?"+q.Encode())
	if err != nil {
		// Fall back to accountId for Cloud instances. Most DC instances
		// return 404 for account-id lookups, so this remains cheap.
		q2 := url.Values{}
		q2.Set("accountId", key)
		body, err = c.get(ctx, "/rest/api/user?"+q2.Encode())
		if err != nil {
			return "", err
		}
	}
	if name, ok := body["displayName"].(string); ok && name != "" {
		return name, nil
	}
	return "", fmt.Errorf("confluence: user %q has no displayName in response", key)
}

// ListSpaces calls GET /rest/api/space and returns the results array.
func (c *ConfluenceClient) ListSpaces(ctx context.Context) ([]map[string]any, error) {
	body, err := c.get(ctx, "/rest/api/space?limit=100&expand=description.plain")
	if err != nil {
		return nil, err
	}
	return extractResults(body)
}

// ListSpacePages calls GET /rest/api/space/{spaceKey}/content and returns pages.
func (c *ConfluenceClient) ListSpacePages(ctx context.Context, spaceKey string) ([]map[string]any, error) {
	body, err := c.get(ctx, "/rest/api/space/"+spaceKey+"/content?type=page&limit=100&expand=ancestors")
	if err != nil {
		return nil, err
	}
	// The content endpoint nests results under body.page.results
	if pageSection, ok := body["page"].(map[string]any); ok {
		return extractResults(pageSection)
	}
	return extractResults(body)
}

// ListPageChildren calls GET /rest/api/content/{pageID}/child/page and returns
// the child pages.
func (c *ConfluenceClient) ListPageChildren(ctx context.Context, pageID string) ([]map[string]any, error) {
	body, err := c.get(ctx, "/rest/api/content/"+pageID+"/child/page?limit=100")
	if err != nil {
		return nil, err
	}
	return extractResults(body)
}

// paginatedResult holds the results and pagination info from a Confluence response.
type paginatedResult struct {
	Results []map[string]any
	HasNext bool // true if _links.next exists (more pages available)
	Size    int  // the "size" field from the response
}

// extractPaginatedResults pulls the "results" array and pagination info from
// a Confluence paginated response body.
func extractPaginatedResults(body map[string]any) paginatedResult {
	pr := paginatedResult{}

	// Check for _links.next — the authoritative "has more" signal.
	if links, ok := body["_links"].(map[string]any); ok {
		if _, ok := links["next"]; ok {
			pr.HasNext = true
		}
	}

	// Extract size field.
	if size, ok := body["size"].(float64); ok {
		pr.Size = int(size)
	}

	// Extract results array.
	raw, ok := body["results"]
	if !ok {
		return pr
	}
	slice, ok := raw.([]any)
	if !ok {
		return pr
	}
	pr.Results = make([]map[string]any, 0, len(slice))
	for _, item := range slice {
		if m, ok := item.(map[string]any); ok {
			pr.Results = append(pr.Results, m)
		}
	}
	return pr
}

// extractResults is a convenience wrapper for code that only needs the results array.
func extractResults(body map[string]any) ([]map[string]any, error) {
	pr := extractPaginatedResults(body)
	return pr.Results, nil
}

// ---------------------------------------------------------------------------
// ConfluencePage holds page metadata returned by the Confluence API.
// ---------------------------------------------------------------------------

// ConfluencePage represents a Confluence page with optional body content.
type ConfluencePage struct {
	ID       string
	Title    string
	BodyHTML string // body.storage.value — populated only by GetPageContent
	Version  struct {
		Number int
		When   string // ISO 8601 timestamp
	}
	WebUILink string // _links.webui
}

// ConfluencePageWithPath augments ConfluencePage with the ordered list of
// ancestor titles (root → direct parent), used by the import-modal search
// flow to render breadcrumbs without a per-page round-trip.
type ConfluencePageWithPath struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	AncestorTitles []string `json:"ancestorTitles"`
}

// parsePageWithAncestors extracts a ConfluencePageWithPath from a raw JSON
// content map. Ancestor titles are returned in API order (root first).
func parsePageWithAncestors(m map[string]any) ConfluencePageWithPath {
	// Initialize as empty (not nil) so JSON marshals to [] instead of null —
	// the frontend import-modal search filter calls .some() on this field.
	p := ConfluencePageWithPath{AncestorTitles: []string{}}
	if id, ok := m["id"].(string); ok {
		p.ID = id
	}
	if title, ok := m["title"].(string); ok {
		p.Title = title
	}
	if anc, ok := m["ancestors"].([]any); ok {
		for _, raw := range anc {
			am, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := am["title"].(string); ok {
				p.AncestorTitles = append(p.AncestorTitles, t)
			}
		}
	}
	return p
}

// parsePageFromMap extracts a ConfluencePage from a raw JSON map.
func parsePageFromMap(m map[string]any) ConfluencePage {
	p := ConfluencePage{}
	if id, ok := m["id"].(string); ok {
		p.ID = id
	}
	if title, ok := m["title"].(string); ok {
		p.Title = title
	}
	if body, ok := m["body"].(map[string]any); ok {
		if storage, ok := body["storage"].(map[string]any); ok {
			if val, ok := storage["value"].(string); ok {
				p.BodyHTML = val
			}
		}
	}
	if ver, ok := m["version"].(map[string]any); ok {
		if n, ok := ver["number"].(float64); ok {
			p.Version.Number = int(n)
		}
		if when, ok := ver["when"].(string); ok {
			p.Version.When = when
		}
	}
	if links, ok := m["_links"].(map[string]any); ok {
		if webui, ok := links["webui"].(string); ok {
			p.WebUILink = webui
		}
	}
	return p
}

// GetPageContent fetches a single page with its body.storage content and version.
func (c *ConfluenceClient) GetPageContent(ctx context.Context, pageID string) (ConfluencePage, error) {
	body, err := c.get(ctx, "/rest/api/content/"+pageID+"?expand=body.storage,version")
	if err != nil {
		return ConfluencePage{}, err
	}
	return parsePageFromMap(body), nil
}

// GetAllSpacePages fetches all pages in a space using pagination.
func (c *ConfluenceClient) GetAllSpacePages(ctx context.Context, spaceKey string) ([]ConfluencePage, error) {
	var pages []ConfluencePage
	start := 0
	const limit = 100

	for {
		path := fmt.Sprintf("/rest/api/content?spaceKey=%s&type=page&start=%d&limit=%d&expand=version",
			spaceKey, start, limit)
		body, err := c.get(ctx, path)
		if err != nil {
			return nil, err
		}
		pr := extractPaginatedResults(body)
		for _, m := range pr.Results {
			pages = append(pages, parsePageFromMap(m))
		}
		if !pr.HasNext {
			break
		}
		start += limit
	}
	return pages, nil
}

// GetAllSpacePagesWithAncestors fetches every page in a space along with each
// page's ancestor chain (root → direct parent). Used by the import-modal
// search/filter flow.
func (c *ConfluenceClient) GetAllSpacePagesWithAncestors(ctx context.Context, spaceKey string) ([]ConfluencePageWithPath, error) {
	var pages []ConfluencePageWithPath
	start := 0
	const limit = 100

	for {
		path := fmt.Sprintf("/rest/api/content?spaceKey=%s&type=page&start=%d&limit=%d&expand=ancestors",
			spaceKey, start, limit)
		body, err := c.get(ctx, path)
		if err != nil {
			return nil, err
		}
		pr := extractPaginatedResults(body)
		for _, m := range pr.Results {
			pages = append(pages, parsePageWithAncestors(m))
		}
		if !pr.HasNext {
			break
		}
		start += limit
	}
	return pages, nil
}

// GetPageTree fetches all descendant pages of a root page using BFS traversal.
func (c *ConfluenceClient) GetPageTree(ctx context.Context, rootPageID string) ([]ConfluencePage, error) {
	var pages []ConfluencePage
	queue := []string{rootPageID}

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		start := 0
		const limit = 100
		for {
			path := fmt.Sprintf("/rest/api/content/%s/child/page?start=%d&limit=%d&expand=version",
				parentID, start, limit)
			body, err := c.get(ctx, path)
			if err != nil {
				return nil, err
			}
			pr := extractPaginatedResults(body)
			for _, m := range pr.Results {
				p := parsePageFromMap(m)
				pages = append(pages, p)
				queue = append(queue, p.ID)
			}
			if !pr.HasNext {
				break
			}
			start += limit
		}
	}
	return pages, nil
}

// ConfluenceAttachment holds metadata for a page attachment.
type ConfluenceAttachment struct {
	ID           string
	Title        string
	MediaType    string
	FileSize     int
	DownloadLink string // _links.download (relative path)
}

// GetPageAttachments fetches attachments for a page with pagination.
func (c *ConfluenceClient) GetPageAttachments(ctx context.Context, pageID string) ([]ConfluenceAttachment, error) {
	var attachments []ConfluenceAttachment
	start := 0
	const limit = 50

	for {
		path := fmt.Sprintf("/rest/api/content/%s/child/attachment?start=%d&limit=%d",
			pageID, start, limit)
		body, err := c.get(ctx, path)
		if err != nil {
			return nil, err
		}
		pr := extractPaginatedResults(body)
		results := pr.Results
		for _, m := range results {
			att := ConfluenceAttachment{}
			if id, ok := m["id"].(string); ok {
				att.ID = id
			}
			if title, ok := m["title"].(string); ok {
				att.Title = title
			}
			if mt, ok := m["metadata"].(map[string]any); ok {
				if mediaType, ok := mt["mediaType"].(string); ok {
					att.MediaType = mediaType
				}
			}
			// mediaType can also be at top level in some Confluence versions
			if att.MediaType == "" {
				if mediaType, ok := m["mediaType"].(string); ok {
					att.MediaType = mediaType
				}
			}
			if ext, ok := m["extensions"].(map[string]any); ok {
				if fs, ok := ext["fileSize"].(float64); ok {
					att.FileSize = int(fs)
				} else if fsStr, ok := ext["fileSize"].(string); ok {
					att.FileSize, _ = strconv.Atoi(fsStr)
				}
				if mt, ok := ext["mediaType"].(string); ok && att.MediaType == "" {
					att.MediaType = mt
				}
			}
			if links, ok := m["_links"].(map[string]any); ok {
				if dl, ok := links["download"].(string); ok {
					att.DownloadLink = dl
				}
			}
			attachments = append(attachments, att)
		}
		if !pr.HasNext {
			break
		}
		start += limit
	}
	return attachments, nil
}

// DownloadAttachment downloads an attachment by its relative download path.
// Returns the raw bytes.
func (c *ConfluenceClient) DownloadAttachment(ctx context.Context, downloadPath string) ([]byte, error) {
	url := c.baseURL + downloadPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("download returned status %d: %w", resp.StatusCode, ErrConfluenceAuthFailed)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download body: %w", err)
	}
	return data, nil
}
