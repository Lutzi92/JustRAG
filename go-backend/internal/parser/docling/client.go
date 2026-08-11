// Package docling provides a Go HTTP client for the Docling Serve sidecar
// (https://github.com/docling-project/docling-serve), and a parser.Parser
// implementation that uses it to extract layout-aware text from PDFs.
//
// The client is intentionally tolerant: all transport errors and non-2xx
// responses bubble up as ordinary errors, so the caller (parser factory)
// can fall back to a simpler PDF path without crashing the ingest pipeline.
package docling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ConvertOptions carries the optional enrichment flags sent on each convert
// request. The zero value sends no extra fields, preserving the legacy request.
type ConvertOptions struct {
	PictureDescription    bool    // do_picture_description: caption images via the configured VLM
	PictureAreaThreshold  float64 // picture_description_area_threshold: skip images below this fraction of page area
	PictureClassification bool    // do_picture_classification: classify pictures (logo/chart/photo)
	TableMode             string  // table_mode: "fast" | "accurate" (empty → server default)

	// Picture-description vision endpoint, injected per-request as
	// picture_description_api so Docling calls our (authenticated) model API.
	// Sourced from the app's AI provider config — the API key is NOT stored on
	// the Docling sidecar.
	PictureAPIURL   string // full OpenAI-compatible chat/completions URL
	PictureAPIModel string // model id sent as params.model (verbatim, e.g. "jlu/gemma-4-26b-it")
	PictureAPIKey   string // bearer token; sent as an Authorization header (omitted when empty)
}

// Client talks to a Docling Serve instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
	// Options is applied to every Convert call. Zero value = legacy request.
	Options ConvertOptions
}

// NewClient returns a Client targeting baseURL with the given request timeout.
// A trailing slash on baseURL is removed.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// BaseURL returns the (trimmed) base URL the client is configured to use.
func (c *Client) BaseURL() string { return c.baseURL }

// DocItem is one content element of the converted document, in reading order,
// together with the source page it was found on. Built from the
// DoclingDocument in the response's json_content: docling-serve reports page
// provenance only there (`prov[].page_no`), never as a per-page text array.
// Callers use these as anchors to map markdown offsets back to pages.
type DocItem struct {
	Text string
	Page int // 1-based; 0 when the item carries no provenance
}

// ConvertResult is the parsed Docling response.
type ConvertResult struct {
	Markdown string
	Text     string
	// Items is the document's content in reading order with page provenance.
	// Empty when Docling returned no json_content (or none of its items carry
	// provenance, as for formats without pagination) — page numbers are then
	// simply unknown.
	Items []DocItem
}

// Convert uploads a document (PDF, DOCX, PPTX, HTML, image) to Docling Serve
// and returns the parsed result. fileName drives Docling's format auto-detection.
func (c *Client) Convert(ctx context.Context, fileName string, r io.Reader) (*ConvertResult, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("files", fileName)
	if err != nil {
		return nil, fmt.Errorf("docling: create form file: %w", err)
	}
	if _, err := io.Copy(fw, r); err != nil {
		return nil, fmt.Errorf("docling: copy file body: %w", err)
	}
	// md carries the content we chunk; json carries the DoclingDocument whose
	// prov[].page_no is the only place docling-serve reports page numbers.
	// to_formats is a repeated field, not a comma list.
	for _, f := range []string{"md", "json"} {
		if err := mw.WriteField("to_formats", f); err != nil {
			return nil, fmt.Errorf("docling: write form field to_formats: %w", err)
		}
	}
	fields := map[string]string{
		"return_response_type": "json",
	}
	if c.Options.PictureDescription {
		fields["do_picture_description"] = "true"
		fields["abort_on_error"] = "false"
		if c.Options.PictureAreaThreshold > 0 {
			fields["picture_description_area_threshold"] = strconv.FormatFloat(c.Options.PictureAreaThreshold, 'g', -1, 64)
		}
		if c.Options.PictureClassification {
			fields["do_picture_classification"] = "true"
		}
		// Point Docling's captioning at our authenticated model API. The key
		// travels in the request headers map, not on the sidecar.
		if c.Options.PictureAPIURL != "" {
			api := map[string]any{"url": c.Options.PictureAPIURL}
			if c.Options.PictureAPIModel != "" {
				api["params"] = map[string]any{"model": c.Options.PictureAPIModel}
			}
			if c.Options.PictureAPIKey != "" {
				api["headers"] = map[string]any{"Authorization": "Bearer " + c.Options.PictureAPIKey}
			}
			if b, err := json.Marshal(api); err == nil {
				fields["picture_description_api"] = string(b)
			}
		}
	}
	if c.Options.TableMode != "" {
		fields["table_mode"] = c.Options.TableMode
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("docling: write form field %s: %w", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("docling: close multipart: %w", err)
	}

	url := c.baseURL + "/v1/convert/file"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("docling: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docling: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("docling: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return parseDoclingResponse(resp.Body)
}

// doclingJSONDoc is the subset of the DoclingDocument (json_content) we read:
// the body/group child references that give reading order, plus the text and
// table items those references point at, each with its page provenance.
type doclingJSONDoc struct {
	Body   doclingNode    `json:"body"`
	Groups []doclingNode  `json:"groups"`
	Texts  []doclingText  `json:"texts"`
	Tables []doclingTable `json:"tables"`
}

type doclingNode struct {
	Children []struct {
		Ref string `json:"$ref"`
	} `json:"children"`
}

type doclingProv []struct {
	PageNo int `json:"page_no"`
}

func (p doclingProv) page() int {
	if len(p) == 0 {
		return 0
	}
	return p[0].PageNo
}

type doclingText struct {
	Text string      `json:"text"`
	Prov doclingProv `json:"prov"`
}

type doclingTable struct {
	Prov doclingProv `json:"prov"`
	Data struct {
		TableCells []struct {
			Text string `json:"text"`
		} `json:"table_cells"`
	} `json:"data"`
}

// anchorText returns a cell text usable as a markdown anchor for the table.
func (t doclingTable) anchorText() string {
	for _, c := range t.Data.TableCells {
		if s := strings.TrimSpace(c.Text); s != "" {
			return s
		}
	}
	return ""
}

func parseDoclingResponse(body io.Reader) (*ConvertResult, error) {
	var raw struct {
		Document struct {
			MdContent   string          `json:"md_content"`
			TextContent string          `json:"text_content"`
			JSONContent json.RawMessage `json:"json_content"`
		} `json:"document"`
		Errors []any `json:"errors"`
	}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("docling: decode response: %w", err)
	}
	res := &ConvertResult{
		Markdown: raw.Document.MdContent,
		Text:     raw.Document.TextContent,
	}
	if res.Markdown == "" && res.Text == "" {
		return nil, fmt.Errorf("docling: response contained no text")
	}
	res.Items = itemsFromJSONContent(raw.Document.JSONContent)
	return res, nil
}

// maxRefDepth bounds group nesting while walking body children, so a
// malformed (cyclic) document cannot spin the walker.
const maxRefDepth = 32

// itemsFromJSONContent walks the DoclingDocument body in reading order and
// returns its text and table items with page provenance. Returns nil when
// json_content is absent, unparseable, or carries no page numbers at all
// (e.g. unpaginated formats) — the caller then treats pages as unknown.
func itemsFromJSONContent(rawJSON json.RawMessage) []DocItem {
	if len(rawJSON) == 0 {
		return nil
	}
	var doc doclingJSONDoc
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		return nil
	}

	var items []DocItem
	seen := make(map[string]bool)

	var walk func(n doclingNode, depth int)
	walk = func(n doclingNode, depth int) {
		if depth > maxRefDepth {
			return
		}
		for _, child := range n.Children {
			ref := child.Ref
			if seen[ref] {
				continue
			}
			seen[ref] = true

			kind, idx, ok := parseSelfRef(ref)
			if !ok {
				continue
			}
			switch kind {
			case "texts":
				if idx < len(doc.Texts) {
					t := doc.Texts[idx]
					if s := strings.TrimSpace(t.Text); s != "" {
						items = append(items, DocItem{Text: s, Page: t.Prov.page()})
					}
				}
			case "tables":
				if idx < len(doc.Tables) {
					tbl := doc.Tables[idx]
					if s := tbl.anchorText(); s != "" {
						items = append(items, DocItem{Text: s, Page: tbl.Prov.page()})
					}
				}
			case "groups":
				if idx < len(doc.Groups) {
					walk(doc.Groups[idx], depth+1)
				}
			}
		}
	}
	walk(doc.Body, 0)

	for _, it := range items {
		if it.Page > 0 {
			return items
		}
	}
	return nil
}

// parseSelfRef splits a DoclingDocument JSON pointer such as "#/texts/3" into
// its collection name and index.
func parseSelfRef(ref string) (kind string, idx int, ok bool) {
	rest, found := strings.CutPrefix(ref, "#/")
	if !found {
		return "", 0, false
	}
	kind, idxStr, found := strings.Cut(rest, "/")
	if !found {
		return "", 0, false
	}
	n, err := strconv.Atoi(idxStr)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return kind, n, true
}
