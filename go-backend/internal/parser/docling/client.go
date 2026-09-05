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
	_ "embed"
	"encoding/json"
	"errors"
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
	PictureClassification bool    // do_picture_classification: classify pictures; implied by PictureDescription
	TableMode             string  // table_mode: "fast" | "accurate" (empty → server default)

	// Picture-description vision endpoint, injected per-request as
	// picture_description_api so Docling calls our (authenticated) model API.
	// Sourced from the app's AI provider config — the API key is NOT stored on
	// the Docling sidecar.
	PictureAPIURL   string // full OpenAI-compatible chat/completions URL
	PictureAPIModel string // model id sent as params.model (verbatim, e.g. "jlu/gemma-4-26b-it")
	PictureAPIKey   string // bearer token; sent as an Authorization header (omitted when empty)

	// Vision-call tuning. Docling's own defaults are a 20-second per-image
	// timeout, one call at a time, and the English prompt "Describe this
	// image in a few sentences." — a timed-out call yields a picture with no
	// description and no error, so the timeout matters more than it looks.
	PicturePrompt         string  // prompt sent with each image (empty → docling default)
	PictureTimeoutSeconds float64 // per-image API timeout (0 → docling default, 20 s)
	PictureConcurrency    int     // parallel vision calls per document (0 → docling default, 1)

	// OCR. Docling's auto engine on Linux is RapidOCR, whose docling-serve
	// default language list is English + Chinese; German scans need "de".
	// Each language is sent as its own repeated ocr_lang field.
	OCRLanguages []string
	ForceOCR     bool // force_ocr: OCR every page, replacing the PDF text layer

	// DocumentTimeoutSeconds bounds the sidecar's processing time per document
	// (document_timeout); 0 leaves the server default, which is a week.
	DocumentTimeoutSeconds float64
}

// pictureClassificationDeny lists docling picture classes that never earn a
// vision call: letterhead, decoration and machine-readable marks. Applied
// only when classification runs, which the client turns on with captioning.
var pictureClassificationDeny = []string{
	"logo", "icon", "signature", "stamp", "bar_code", "qr_code", "page_thumbnail",
}

// Client talks to a Docling Serve instance.
type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	// Async routes conversions through the task endpoints (submit → poll →
	// result) instead of the synchronous /v1/convert/file. The sync endpoint
	// gives up after the sidecar's DOCLING_SERVE_MAX_SYNC_WAIT (upstream
	// default 120 s) with a 504 no matter how long the client would wait; the
	// task endpoints have no such cap. A sidecar without them (404 on submit)
	// is served synchronously.
	Async bool
	// PollInterval is the wait between status polls in Async mode
	// (default 2 s).
	PollInterval time.Duration
	// Options is applied to every Convert call when OptionsFunc is nil.
	// Zero value = legacy request.
	Options ConvertOptions
	// OptionsFunc, when set, is consulted on every Convert call instead of
	// Options, so admin-panel changes reach the next conversion rather than
	// the next worker restart.
	OptionsFunc func(ctx context.Context) ConvertOptions
}

// probePDF is a one-line, 600-byte PDF used by Probe.
//
//go:embed testdata/probe.pdf
var probePDF []byte

// Probe converts a tiny embedded PDF with the client's live options and
// returns the error the real conversions would hit. Its purpose is the
// failure that is otherwise invisible: a sidecar started without
// DOCLING_SERVE_ENABLE_REMOTE_SERVICES rejects every captioning request at
// pipeline construction, and the fallback parser then quietly routes every
// document to the built-in parsers.
func (c *Client) Probe(ctx context.Context) error {
	_, err := c.Convert(ctx, "justrag-docling-probe.pdf", bytes.NewReader(probePDF))
	return err
}

// options returns the options for this call.
func (c *Client) options(ctx context.Context) ConvertOptions {
	if c.OptionsFunc != nil {
		return c.OptionsFunc(ctx)
	}
	return c.Options
}

// NewClient returns a Client targeting baseURL with the given request timeout.
// A trailing slash on baseURL is removed.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
		timeout:    timeout,
	}
}

// BaseURL returns the (trimmed) base URL the client is configured to use.
func (c *Client) BaseURL() string { return c.baseURL }

// DocItem is one content element of the converted document, in reading order,
// with the source page it came from. Built from the DoclingDocument in the
// response's json_content: docling-serve reports page provenance only there
// (`prov[].page_no`), never as a per-page text array.
//
// These items — not the markdown blob — are the source of truth for per-page
// text. Recovering page boundaries by searching item text back in md_content
// cannot work: docling escapes markdown metacharacters (`max_value` becomes
// `max\_value`), drops furniture entirely, and re-renders tables, so a
// significant share of items never match, and each miss shifts every later
// page's boundary.
type DocItem struct {
	Page  int    // 1-based; 0 when the item carries no provenance
	Label string // docling label: "text", "section_header", "list_item", "table", …
	// Level is the heading depth of a section_header (1 = top). 0 when the
	// sidecar reported none, which renders like level 1.
	Level int
	// Furniture marks running headers/footers and page numbers. Docling keeps
	// them out of its own markdown; we drop them too, so repeated boilerplate
	// doesn't land in every page's chunks.
	Furniture bool
	Text      string      // content for text-ish items; empty for tables and pictures
	Table     *DocTable   // non-nil for table items
	Picture   *DocPicture // non-nil for picture items
}

// DocPicture is a figure's text content: the caption printed in the document
// and, when picture description is enabled, the vision model's description of
// the image. At least one of the two is non-empty — a picture carrying neither
// contributes nothing to chunk and is never emitted as an item.
type DocPicture struct {
	Caption     string
	Description string
	// InnerText is the text docling found inside the figure's own bounding
	// box — axis values, bar labels, legend entries. For a vector chart these
	// are the real numbers, and docling keeps them out of its markdown.
	InnerText string
	Footnotes []string
}

// DocTable is a table's cell grid, enough to render it as markdown.
type DocTable struct {
	NumRows   int
	NumCols   int
	Cells     []DocTableCell
	Caption   string   // the caption printed above or below the table
	Footnotes []string // table footnotes ("* Stand 2025")
}

// DocTableCell is one cell, positioned by its top-left offsets.
type DocTableCell struct {
	Text   string
	Row    int
	Col    int
	Header bool // part of the column header row
}

// ConvertResult is the parsed Docling response.
type ConvertResult struct {
	Markdown string
	Text     string
	// Confidence is docling's own quality estimate for the conversion
	// (docling-serve ≥ 1.25); nil when the sidecar sent none.
	Confidence *ConfidenceScores
	// Items is the document's content in reading order with page provenance.
	// Empty when Docling returned no json_content (or none of its items carry
	// provenance, as for formats without pagination) — page numbers are then
	// simply unknown.
	Items []DocItem
}

// ConfidenceScores mirrors docling's per-document confidence block. Scores
// are in [0,1] and nil when the stage did not run (no tables, no OCR);
// grades are docling's buckets: poor, fair, good, excellent, unspecified.
type ConfidenceScores struct {
	ParseScore  *float64 `json:"parse_score"`
	LayoutScore *float64 `json:"layout_score"`
	TableScore  *float64 `json:"table_score"`
	OCRScore    *float64 `json:"ocr_score"`
	MeanScore   *float64 `json:"mean_score"`
	LowScore    *float64 `json:"low_score"`
	MeanGrade   string   `json:"mean_grade"`
	LowGrade    string   `json:"low_grade"`
}

// Convert uploads a document (PDF, DOCX, PPTX, HTML, image) to Docling Serve
// and returns the parsed result. fileName drives Docling's format auto-detection.
func (c *Client) Convert(ctx context.Context, fileName string, r io.Reader) (*ConvertResult, error) {
	opts := c.options(ctx)
	form, contentType, err := buildConvertForm(opts, fileName, r)
	if err != nil {
		return nil, err
	}
	if c.Async {
		res, err := c.convertAsync(ctx, form, contentType)
		if !errors.Is(err, errAsyncUnavailable) {
			return res, err
		}
	}
	return c.convertSync(ctx, form, contentType)
}

// buildConvertForm renders the multipart request body shared by the sync
// and async convert endpoints.
func buildConvertForm(opts ConvertOptions, fileName string, r io.Reader) ([]byte, string, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("files", fileName)
	if err != nil {
		return nil, "", fmt.Errorf("docling: create form file: %w", err)
	}
	if _, err := io.Copy(fw, r); err != nil {
		return nil, "", fmt.Errorf("docling: copy file body: %w", err)
	}
	// md carries the content we chunk; json carries the DoclingDocument whose
	// prov[].page_no is the only place docling-serve reports page numbers.
	// to_formats is a repeated field, not a comma list.
	for _, f := range []string{"md", "json"} {
		if err := mw.WriteField("to_formats", f); err != nil {
			return nil, "", fmt.Errorf("docling: write form field to_formats: %w", err)
		}
	}
	// Repeated fields: one multipart value per language.
	for _, lang := range opts.OCRLanguages {
		if lang = strings.TrimSpace(lang); lang != "" {
			if err := mw.WriteField("ocr_lang", lang); err != nil {
				return nil, "", fmt.Errorf("docling: write form field ocr_lang: %w", err)
			}
		}
	}
	fields := map[string]string{
		"return_response_type": "json",
		// A failed enrichment sub-step (one picture the vision model could
		// not describe) must not fail the document.
		"abort_on_error": "false",
		// Off by default on the sidecar, which flattens every heading to
		// level 1 and with it the `sections` chunk metadata built from them.
		"do_pdf_heading_hierarchy": "true",
	}
	if opts.ForceOCR {
		fields["force_ocr"] = "true"
	}
	if opts.DocumentTimeoutSeconds > 0 {
		fields["document_timeout"] = strconv.FormatFloat(opts.DocumentTimeoutSeconds, 'g', -1, 64)
	}
	if opts.PictureDescription {
		fields["do_picture_description"] = "true"
		if opts.PictureAreaThreshold > 0 {
			fields["picture_description_area_threshold"] = strconv.FormatFloat(opts.PictureAreaThreshold, 'g', -1, 64)
		}
		// Classification is what the deny-list below keys on, so captioning
		// always turns it on; it is a cheap local model.
		fields["do_picture_classification"] = "true"
		// Point Docling's captioning at our authenticated model API. The key
		// travels in the request headers map, not on the sidecar.
		if opts.PictureAPIURL != "" {
			api := map[string]any{"url": opts.PictureAPIURL}
			if opts.PictureAPIModel != "" {
				api["params"] = map[string]any{"model": opts.PictureAPIModel}
			}
			if opts.PictureAPIKey != "" {
				api["headers"] = map[string]any{"Authorization": "Bearer " + opts.PictureAPIKey}
			}
			if opts.PicturePrompt != "" {
				api["prompt"] = opts.PicturePrompt
			}
			if opts.PictureTimeoutSeconds > 0 {
				api["timeout"] = opts.PictureTimeoutSeconds
			}
			if opts.PictureConcurrency > 0 {
				api["concurrency"] = opts.PictureConcurrency
			}
			api["classification_deny"] = pictureClassificationDeny
			if b, err := json.Marshal(api); err == nil {
				fields["picture_description_api"] = string(b)
			}
		}
	}
	if opts.PictureClassification && !opts.PictureDescription {
		fields["do_picture_classification"] = "true"
	}
	if opts.TableMode != "" {
		fields["table_mode"] = opts.TableMode
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, "", fmt.Errorf("docling: write form field %s: %w", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("docling: close multipart: %w", err)
	}

	return body.Bytes(), mw.FormDataContentType(), nil
}

// convertSync posts to /v1/convert/file and decodes the response.
func (c *Client) convertSync(ctx context.Context, form []byte, contentType string) (*ConvertResult, error) {
	resp, err := c.post(ctx, "/v1/convert/file", form, contentType)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	return parseDoclingResponse(resp.Body)
}

// errAsyncUnavailable signals that the sidecar has no task endpoints.
var errAsyncUnavailable = errors.New("docling: async endpoints unavailable")

// convertAsync submits the document as a task, polls its status and fetches
// the result. The whole exchange is bounded by the client timeout, the same
// budget the sync path had per request. A 404 on submit means an older
// sidecar; anything else that is not a task is an error, never a silent
// sync retry — a 422 there is a field the sidecar does not know.
func (c *Client) convertAsync(ctx context.Context, form []byte, contentType string) (*ConvertResult, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.post(ctx, "/v1/convert/file/async", form, contentType)
	if err != nil {
		return nil, err
	}
	var task taskStatus
	func() {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			err = errAsyncUnavailable
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = statusError(resp)
			return
		}
		err = json.NewDecoder(resp.Body).Decode(&task)
	}()
	if err != nil {
		if errors.Is(err, errAsyncUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("docling: submit task: %w", err)
	}
	if task.TaskID == "" {
		return nil, fmt.Errorf("docling: submit task: response carried no task_id")
	}

	interval := c.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		st, err := c.pollTask(ctx, task.TaskID)
		if err != nil {
			return nil, err
		}
		switch st.TaskStatus {
		case "success":
			return c.fetchResult(ctx, task.TaskID)
		case "failure":
			msg := st.ErrorMessage
			if msg == "" && st.Failure != nil {
				msg = st.Failure.Message
			}
			if msg == "" {
				msg = "task failed"
			}
			return nil, fmt.Errorf("docling: task %s failed: %s", task.TaskID, msg)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("docling: waiting for task %s: %w", task.TaskID, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// taskStatus is the subset of docling-serve's TaskStatusResponse we read.
type taskStatus struct {
	TaskID       string `json:"task_id"`
	TaskStatus   string `json:"task_status"` // pending | started | success | failure
	ErrorMessage string `json:"error_message"`
	Failure      *struct {
		Message string `json:"message"`
	} `json:"failure"`
}

func (c *Client) pollTask(ctx context.Context, id string) (*taskStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/status/poll/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("docling: build poll request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docling: poll task %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	var st taskStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("docling: decode task status: %w", err)
	}
	return &st, nil
}

func (c *Client) fetchResult(ctx context.Context, id string) (*ConvertResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/result/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("docling: build result request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docling: fetch result %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	return parseDoclingResponse(resp.Body)
}

// post sends a multipart body to path.
func (c *Client) post(ctx context.Context, path string, form []byte, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(form))
	if err != nil {
		return nil, fmt.Errorf("docling: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docling: do request: %w", err)
	}
	return resp, nil
}

// statusError renders a non-2xx response as an error carrying the status
// and a snippet of the body.
func statusError(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("docling: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// doclingJSONDoc is the subset of the DoclingDocument (json_content) we read:
// the body/group child references that give reading order, plus the text and
// table items those references point at, each with its page provenance.
type doclingJSONDoc struct {
	Body     doclingNode      `json:"body"`
	Groups   []doclingNode    `json:"groups"`
	Texts    []doclingText    `json:"texts"`
	Tables   []doclingTable   `json:"tables"`
	Pictures []doclingPicture `json:"pictures"`
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
	Text         string      `json:"text"`
	Label        string      `json:"label"`
	Level        int         `json:"level"`
	ContentLayer string      `json:"content_layer"`
	Prov         doclingProv `json:"prov"`
}

// contentLayerFurniture is docling's marker for page furniture (running
// headers, footers, page numbers) — content it keeps out of its own markdown.
const contentLayerFurniture = "furniture"

// doclingPicture is the subset of a DoclingDocument picture item we read.
//
// A picture reaches the page as text two ways, and both used to be dropped:
// its caption, which is a reference into texts rather than a body child of its
// own, and the vision model's description written by docling's
// picture-description enrichment.
//
// The description has two homes across docling-core versions — the original
// `annotations` list, which upstream now marks deprecated, and `meta.description`
// — so both are read. Reading only one would repeat this package's recurring
// failure: a response-shape assumption that holds until the sidecar is upgraded
// and then silently drops content while every unit test stays green.
type doclingPicture struct {
	Label        string       `json:"label"`
	ContentLayer string       `json:"content_layer"`
	Prov         doclingProv  `json:"prov"`
	Captions     []doclingRef `json:"captions"`
	Footnotes    []doclingRef `json:"footnotes"`
	// Children are text items whose parent is the picture: the text docling
	// detected inside the figure's bounding box.
	Children    []doclingRef `json:"children"`
	Annotations []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"annotations"`
	Meta struct {
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
	} `json:"meta"`
}

// annotationKindDescription is the discriminator docling sets on the
// picture-description annotation; the same list also carries classification
// and chart-data entries, which are not text.
const annotationKindDescription = "description"

// descriptionText returns the vision model's description of the picture, or ""
// when it carries none — captioning disabled, the image below
// picture_description_area_threshold, or the model call having failed
// (abort_on_error=false means a failed caption is an absent one, not an error).
// meta wins over the deprecated annotations list when both are present.
func (p doclingPicture) descriptionText() string {
	if s := strings.TrimSpace(p.Meta.Description.Text); s != "" {
		return s
	}
	for _, a := range p.Annotations {
		if a.Kind != annotationKindDescription {
			continue
		}
		if s := strings.TrimSpace(a.Text); s != "" {
			return s
		}
	}
	return ""
}

// doclingRef is a JSON pointer into one of the document's collections.
type doclingRef struct {
	Ref string `json:"$ref"`
}

type doclingTable struct {
	Label        string       `json:"label"`
	ContentLayer string       `json:"content_layer"`
	Prov         doclingProv  `json:"prov"`
	Captions     []doclingRef `json:"captions"`
	Footnotes    []doclingRef `json:"footnotes"`
	Data         struct {
		NumRows    int `json:"num_rows"`
		NumCols    int `json:"num_cols"`
		TableCells []struct {
			Text         string `json:"text"`
			StartRow     int    `json:"start_row_offset_idx"`
			StartCol     int    `json:"start_col_offset_idx"`
			ColumnHeader bool   `json:"column_header"`
		} `json:"table_cells"`
	} `json:"data"`
}

// toDocTable converts the response's cell list into a DocTable. Returns nil
// when the table carries no cell text at all, so empty detections don't emit
// an empty markdown table into the page.
func (t doclingTable) toDocTable() *DocTable {
	out := &DocTable{NumRows: t.Data.NumRows, NumCols: t.Data.NumCols}
	any := false
	for _, c := range t.Data.TableCells {
		text := strings.TrimSpace(c.Text)
		if text != "" {
			any = true
		}
		out.Cells = append(out.Cells, DocTableCell{
			Text:   text,
			Row:    c.StartRow,
			Col:    c.StartCol,
			Header: c.ColumnHeader,
		})
	}
	if !any {
		return nil
	}
	return out
}

func parseDoclingResponse(body io.Reader) (*ConvertResult, error) {
	var raw struct {
		Document struct {
			MdContent   string          `json:"md_content"`
			TextContent string          `json:"text_content"`
			JSONContent json.RawMessage `json:"json_content"`
		} `json:"document"`
		Errors     []any             `json:"errors"`
		Confidence *ConfidenceScores `json:"confidence"`
	}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("docling: decode response: %w", err)
	}
	res := &ConvertResult{
		Markdown:   raw.Document.MdContent,
		Text:       raw.Document.TextContent,
		Confidence: raw.Confidence,
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
						items = append(items, DocItem{
							Page:      t.Prov.page(),
							Label:     t.Label,
							Level:     t.Level,
							Furniture: t.ContentLayer == contentLayerFurniture,
							Text:      s,
						})
					}
				}
			case "tables":
				if idx < len(doc.Tables) {
					tbl := doc.Tables[idx]
					if table := tbl.toDocTable(); table != nil {
						table.Caption = strings.Join(resolveTexts(tbl.Captions, doc.Texts, seen), " ")
						table.Footnotes = resolveTexts(tbl.Footnotes, doc.Texts, seen)
						items = append(items, DocItem{
							Page:      tbl.Prov.page(),
							Label:     "table",
							Furniture: tbl.ContentLayer == contentLayerFurniture,
							Table:     table,
						})
					}
				}
			case "pictures":
				if idx < len(doc.Pictures) {
					if item, ok := pictureItem(doc.Pictures[idx], doc.Texts, seen); ok {
						items = append(items, item)
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

// pictureItem builds the DocItem for one picture, resolving its caption,
// footnote and child-text refs against the document's text items. Resolved
// refs are marked seen so a caption that is *also* listed as a body child
// lands on the page once rather than twice.
//
// ok is false when the picture carries no text at all: there is nothing to
// chunk, and an empty item would only add a blank paragraph.
func pictureItem(pic doclingPicture, texts []doclingText, seen map[string]bool) (DocItem, bool) {
	caption := strings.Join(resolveTexts(pic.Captions, texts, seen), " ")
	inner := strings.Join(resolveTexts(pic.Children, texts, seen), " ")
	footnotes := resolveTexts(pic.Footnotes, texts, seen)
	description := pic.descriptionText()
	if caption == "" && description == "" && inner == "" && len(footnotes) == 0 {
		return DocItem{}, false
	}
	return DocItem{
		Page:      pic.Prov.page(),
		Label:     "picture",
		Furniture: pic.ContentLayer == contentLayerFurniture,
		Picture: &DocPicture{
			Caption:     caption,
			Description: description,
			InnerText:   inner,
			Footnotes:   footnotes,
		},
	}, true
}

// resolveTexts follows refs into texts and returns their non-empty, trimmed
// contents in order, marking each ref seen. Refs already seen (emitted as a
// body child earlier in the walk) and refs into anything but texts are
// skipped; nil is returned when nothing resolves.
func resolveTexts(refs []doclingRef, texts []doclingText, seen map[string]bool) []string {
	var out []string
	for _, ref := range refs {
		if seen[ref.Ref] {
			continue
		}
		seen[ref.Ref] = true
		kind, idx, ok := parseSelfRef(ref.Ref)
		if !ok || kind != "texts" || idx >= len(texts) {
			continue
		}
		if s := strings.TrimSpace(texts[idx].Text); s != "" {
			out = append(out, s)
		}
	}
	return out
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
