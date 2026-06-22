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

// Page is a single page entry produced by Docling.
type Page struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// ConvertResult is the parsed Docling response.
type ConvertResult struct {
	Markdown string
	Text     string
	Pages    []Page
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
	fields := map[string]string{
		"to_formats":           "md",
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

func parseDoclingResponse(body io.Reader) (*ConvertResult, error) {
	var raw struct {
		Document struct {
			MdContent   string `json:"md_content"`
			TextContent string `json:"text_content"`
			Pages       []struct {
				Number int    `json:"number"`
				Text   string `json:"text"`
			} `json:"pages"`
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
	for _, p := range raw.Document.Pages {
		res.Pages = append(res.Pages, Page{Number: p.Number, Text: p.Text})
	}
	if len(res.Pages) == 0 {
		text := res.Markdown
		if text == "" {
			text = res.Text
		}
		res.Pages = []Page{{Number: 1, Text: text}}
	}
	return res, nil
}
