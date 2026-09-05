package docling

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClient_Convert_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content":   "# Title\n\nBody text\n",
				"text_content": "Title\n\nBody text\n",
			},
			"errors": []any{},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("%PDF-1.4 stub"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Markdown, "Title") {
		t.Errorf("expected markdown to contain Title, got %q", res.Markdown)
	}
	// docling-serve returns no page provenance without json_content: pages
	// are unknown, and must not be fabricated as page 1.
	if len(res.Items) != 0 {
		t.Errorf("expected no items without json_content, got %+v", res.Items)
	}
}

// doclingJSONContent builds a json_content payload in the shape docling-serve
// actually returns (DoclingDocument: body children referencing texts/tables,
// each item carrying prov[].page_no).
// Each item may set "label" (default "text") and "layer" (default "body") to
// mirror the DoclingDocument fields that decide rendering and furniture drops.
func doclingJSONContent(items []map[string]any) map[string]any {
	children := make([]map[string]any, 0, len(items))
	texts := make([]map[string]any, 0, len(items))
	for i, it := range items {
		label, ok := it["label"].(string)
		if !ok {
			label = "text"
		}
		layer, ok := it["layer"].(string)
		if !ok {
			layer = "body"
		}
		children = append(children, map[string]any{"$ref": "#/texts/" + strconv.Itoa(i)})
		texts = append(texts, map[string]any{
			"self_ref":      "#/texts/" + strconv.Itoa(i),
			"label":         label,
			"content_layer": layer,
			"text":          it["text"],
			"prov":          []map[string]any{{"page_no": it["page"], "bbox": map[string]any{}, "charspan": []int{0, 1}}},
		})
	}
	return map[string]any{
		"schema_name": "DoclingDocument",
		"body":        map[string]any{"self_ref": "#/body", "children": children},
		"texts":       texts,
		"pages": map[string]any{
			"1": map[string]any{"page_no": 1, "size": map[string]any{"width": 612, "height": 792}},
		},
	}
}

func TestClient_Convert_ExtractsPageProvenanceFromJSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "intro on one\n\nbody on two\n\ntail on three",
				"json_content": doclingJSONContent([]map[string]any{
					{"text": "intro on one", "page": 1},
					{"text": "body on two", "page": 2},
					{"text": "tail on three", "page": 3},
				}),
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []DocItem{
		{Text: "intro on one", Page: 1, Label: "text"},
		{Text: "body on two", Page: 2, Label: "text"},
		{Text: "tail on three", Page: 3, Label: "text"},
	}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items mismatch:\n got %+v\nwant %+v", res.Items, want)
	}
}

func TestClient_Convert_WalksGroupsAndTablesInReadingOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "heading\n\n- item\n\n| cell |",
				"json_content": map[string]any{
					"body": map[string]any{"children": []map[string]any{
						{"$ref": "#/texts/0"},
						{"$ref": "#/groups/0"},
						{"$ref": "#/tables/0"},
					}},
					"groups": []map[string]any{
						{"children": []map[string]any{{"$ref": "#/texts/1"}}},
					},
					"texts": []map[string]any{
						{"text": "heading", "label": "section_header", "prov": []map[string]any{{"page_no": 1}}},
						{"text": "item", "label": "list_item", "prov": []map[string]any{{"page_no": 2}}},
					},
					"tables": []map[string]any{
						{
							"label": "table",
							"prov":  []map[string]any{{"page_no": 3}},
							"data": map[string]any{
								"num_rows": 1, "num_cols": 2,
								"table_cells": []map[string]any{
									{"text": "links", "start_row_offset_idx": 0, "start_col_offset_idx": 0},
									{"text": "rechts", "start_row_offset_idx": 0, "start_col_offset_idx": 1},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []DocItem{
		{Text: "heading", Page: 1, Label: "section_header"},
		{Text: "item", Page: 2, Label: "list_item"},
		{Page: 3, Label: "table", Table: &DocTable{
			NumRows: 1, NumCols: 2,
			Cells: []DocTableCell{{Text: "links", Row: 0, Col: 0}, {Text: "rechts", Row: 0, Col: 1}},
		}},
	}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items mismatch:\n got %+v\nwant %+v", res.Items, want)
	}
}

func TestClient_Convert_IgnoresJSONContentWithoutProvenance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{
				"md_content": "body",
				"json_content": map[string]any{
					"body":  map[string]any{"children": []map[string]any{{"$ref": "#/texts/0"}}},
					"texts": []map[string]any{{"text": "body"}},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	res, _ := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if len(res.Items) != 0 {
		t.Errorf("expected no items when no prov carries a page, got %+v", res.Items)
	}
}

func TestClient_Convert_RequestsMarkdownAndJSONFormats(t *testing.T) {
	var gotFormats []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		gotFormats = r.MultipartForm.Value["to_formats"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{"md_content": "x"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// json is required: it is the only carrier of page provenance.
	if !reflect.DeepEqual(gotFormats, []string{"md", "json"}) {
		t.Errorf("expected to_formats [md json], got %v", gotFormats)
	}
}

func TestClient_Convert_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	_, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestClient_Convert_TimeoutPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 50*time.Millisecond)
	_, err := c.Convert(context.Background(), "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestClient_Convert_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	c := NewClient(srv.URL, 10*time.Second)
	_, err := c.Convert(ctx, "test.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected ctx-cancel error, got nil")
	}
}

func TestNewClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	c := NewClient("http://example.com/", 1*time.Second)
	if c.BaseURL() != "http://example.com" {
		t.Errorf("expected trimmed URL, got %q", c.BaseURL())
	}
}

func TestClient_EmptyResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, 1*time.Second)
	_, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error for empty document, got nil")
	}
}

func TestClient_Convert_EmitsPictureDescriptionFields(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/convert/file") {
			t.Errorf("expected /v1/convert/file, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"document": map[string]any{"md_content": "# T"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second)
	c.Options = ConvertOptions{
		PictureDescription:    true,
		PictureAreaThreshold:  0.05,
		PictureClassification: true,
		TableMode:             "accurate",
		PictureAPIURL:         "http://model.local/v1/chat/completions",
		PictureAPIModel:       "jlu/gemma-4-26b-it",
		PictureAPIKey:         "secret-key",
	}
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got["do_picture_description"] != "true" {
		t.Errorf("missing do_picture_description, got %+v", got)
	}
	if got["picture_description_area_threshold"] != "0.05" {
		t.Errorf("bad area threshold: %q", got["picture_description_area_threshold"])
	}
	if got["do_picture_classification"] != "true" {
		t.Errorf("missing do_picture_classification")
	}
	if got["table_mode"] != "accurate" {
		t.Errorf("bad table_mode: %q", got["table_mode"])
	}
	if got["abort_on_error"] != "false" {
		t.Errorf("expected abort_on_error=false")
	}
	// picture_description_api must carry the endpoint, model, and bearer header
	// so Docling can call our authenticated model API.
	var api struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Params  map[string]any    `json:"params"`
	}
	if err := json.Unmarshal([]byte(got["picture_description_api"]), &api); err != nil {
		t.Fatalf("picture_description_api not valid JSON: %q (%v)", got["picture_description_api"], err)
	}
	if api.URL != "http://model.local/v1/chat/completions" {
		t.Errorf("bad api url: %q", api.URL)
	}
	if api.Headers["Authorization"] != "Bearer secret-key" {
		t.Errorf("missing/!= bearer header: %q", api.Headers["Authorization"])
	}
	if api.Params["model"] != "jlu/gemma-4-26b-it" {
		t.Errorf("bad api model: %v", api.Params["model"])
	}
}

func TestClient_Convert_PictureAPIOmittedWhenNoKey(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{"md_content": "# T"}})
	}))
	defer srv.Close()

	// PictureDescription on but no endpoint resolved → enrichment flag is sent,
	// but no picture_description_api field (Docling would fall back to its own
	// default model config, or skip — never our request without a URL).
	c := NewClient(srv.URL, 10*time.Second)
	c.Options = ConvertOptions{PictureDescription: true}
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got["do_picture_description"] != "true" {
		t.Errorf("expected do_picture_description=true")
	}
	if _, ok := got["picture_description_api"]; ok {
		t.Errorf("picture_description_api should be omitted with no URL")
	}
}

func TestClient_Convert_ZeroOptionsOmitsNewFields(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		got = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				got[k] = v[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document": map[string]any{"md_content": "# T"}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 10*time.Second) // zero Options
	if _, err := c.Convert(context.Background(), "x.pdf", strings.NewReader("x")); err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, k := range []string{"do_picture_description", "picture_description_area_threshold", "do_picture_classification", "table_mode"} {
		if _, ok := got[k]; ok {
			t.Errorf("field %q should be omitted with zero options", k)
		}
	}
}

// --- picture items -----------------------------------------------------
//
// Shapes below are docling-core's, not invented: a picture's description has
// two homes across versions — the original `annotations[]` list (upstream now
// marks the field deprecated) and `meta.description` — and its caption is a
// `$ref` into `texts`, reachable only through the picture.

const picLegacyAnnotationsDoc = `{
  "schema_name": "DoclingDocument",
  "body": {"self_ref": "#/body", "children": [
    {"$ref": "#/texts/0"},
    {"$ref": "#/pictures/0"}
  ]},
  "texts": [
    {"self_ref": "#/texts/0", "label": "text", "content_layer": "body",
     "text": "Vor der Abbildung.", "prov": [{"page_no": 4}]},
    {"self_ref": "#/texts/1", "label": "caption", "content_layer": "body",
     "text": "Abbildung 3: Stoerungsmeldungen je Monat.", "prov": [{"page_no": 4}]}
  ],
  "pictures": [
    {"self_ref": "#/pictures/0", "label": "picture", "content_layer": "body",
     "prov": [{"page_no": 4}],
     "captions": [{"$ref": "#/texts/1"}],
     "annotations": [
       {"kind": "classification", "predicted_classes": [{"class_name": "bar_chart"}]},
       {"kind": "description", "text": "Ein Balkendiagramm; die Meldungen steigen von 12 auf 47.",
        "provenance": "jlu/gemma-4-26b-it"}
     ]}
  ]
}`

func TestItemsFromJSONContent_PictureLegacyAnnotationCarriesCaptionAndDescription(t *testing.T) {
	items := itemsFromJSONContent([]byte(picLegacyAnnotationsDoc))

	want := []DocItem{
		{Text: "Vor der Abbildung.", Page: 4, Label: "text"},
		{Page: 4, Label: "picture", Picture: &DocPicture{
			Caption:     "Abbildung 3: Stoerungsmeldungen je Monat.",
			Description: "Ein Balkendiagramm; die Meldungen steigen von 12 auf 47.",
		}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items mismatch:\n got %+v\nwant %+v", spew(items), spew(want))
	}
}

func TestItemsFromJSONContent_PictureMetaDescriptionIsRead(t *testing.T) {
	// docling-core deprecated `annotations` in favour of `meta`. A sidecar
	// upgrade must not silently stop producing captions, which is exactly the
	// class of failure this package already shipped twice.
	doc := `{
	  "body": {"children": [{"$ref": "#/pictures/0"}]},
	  "texts": [],
	  "pictures": [
	    {"label": "picture", "content_layer": "body", "prov": [{"page_no": 2}],
	     "meta": {"description": {"text": "Ein Netzplan mit drei Standorten.",
	                              "created_by": "jlu/gemma-4-26b-it"}}}
	  ]
	}`
	items := itemsFromJSONContent([]byte(doc))

	if len(items) != 1 || items[0].Picture == nil {
		t.Fatalf("expected one picture item, got %v", spew(items))
	}
	if got := items[0].Picture.Description; got != "Ein Netzplan mit drei Standorten." {
		t.Errorf("meta.description.text not read, got %q", got)
	}
	if items[0].Page != 2 {
		t.Errorf("picture page = %d, want 2", items[0].Page)
	}
}

func TestItemsFromJSONContent_PictureCaptionIsNotEmittedTwice(t *testing.T) {
	// A caption that is *also* listed as a body child must land on the page
	// once, not once as its own text item and again inside the picture.
	doc := `{
	  "body": {"children": [{"$ref": "#/pictures/0"}, {"$ref": "#/texts/0"}]},
	  "texts": [
	    {"label": "caption", "content_layer": "body", "text": "Abbildung 1: Aufbau.",
	     "prov": [{"page_no": 1}]}
	  ],
	  "pictures": [
	    {"label": "picture", "content_layer": "body", "prov": [{"page_no": 1}],
	     "captions": [{"$ref": "#/texts/0"}],
	     "annotations": [{"kind": "description", "text": "Ein Schema."}]}
	  ]
	}`
	items := itemsFromJSONContent([]byte(doc))

	if len(items) != 1 {
		t.Fatalf("caption must not be emitted a second time, got %v", spew(items))
	}
	if items[0].Picture == nil || items[0].Picture.Caption != "Abbildung 1: Aufbau." {
		t.Fatalf("caption missing from picture item: %v", spew(items))
	}
}

func TestItemsFromJSONContent_PictureWithoutTextIsSkipped(t *testing.T) {
	// Captioning off, or the image fell below picture_description_area_threshold:
	// there is nothing to chunk, so no empty item may reach the page.
	doc := `{
	  "body": {"children": [{"$ref": "#/pictures/0"}, {"$ref": "#/texts/0"}]},
	  "texts": [{"label": "text", "content_layer": "body", "text": "Nur Text.",
	             "prov": [{"page_no": 1}]}],
	  "pictures": [{"label": "picture", "content_layer": "body", "prov": [{"page_no": 1}]}]
	}`
	items := itemsFromJSONContent([]byte(doc))

	if len(items) != 1 || items[0].Text != "Nur Text." {
		t.Fatalf("empty picture must be skipped, got %v", spew(items))
	}
}

func TestItemsFromJSONContent_FurniturePictureIsMarked(t *testing.T) {
	// A logo in the running header is furniture; buildPages drops it like any
	// other furniture rather than captioning it onto every page.
	doc := `{
	  "body": {"children": [{"$ref": "#/pictures/0"}]},
	  "texts": [],
	  "pictures": [
	    {"label": "picture", "content_layer": "furniture", "prov": [{"page_no": 1}],
	     "annotations": [{"kind": "description", "text": "Das Logo der Universitaet."}]}
	  ]
	}`
	items := itemsFromJSONContent([]byte(doc))

	if len(items) != 1 || !items[0].Furniture {
		t.Fatalf("furniture picture must be marked as such, got %v", spew(items))
	}
}

// spew renders items readably, including the pointer-valued Picture field that
// %+v would print as an address.
func spew(items []DocItem) string {
	var sb strings.Builder
	for _, it := range items {
		fmt.Fprintf(&sb, "{Page:%d Label:%q Furniture:%v Text:%q", it.Page, it.Label, it.Furniture, it.Text)
		if it.Picture != nil {
			fmt.Fprintf(&sb, " Picture:{Caption:%q Description:%q}", it.Picture.Caption, it.Picture.Description)
		}
		if it.Table != nil {
			fmt.Fprintf(&sb, " Table:%dx%d", it.Table.NumRows, it.Table.NumCols)
		}
		sb.WriteString("} ")
	}
	return sb.String()
}

func TestItemsFromJSONContent_CaptionListedBeforeItsPictureIsNotRepeated(t *testing.T) {
	// Sibling of the test above, with the body order reversed. That order is
	// what actually exercises the seen-check *read* inside pictureItem: the
	// caption is emitted as its own text item first, so the picture must not
	// append it a second time.
	doc := `{
	  "body": {"children": [{"$ref": "#/texts/0"}, {"$ref": "#/pictures/0"}]},
	  "texts": [
	    {"label": "caption", "content_layer": "body", "text": "Abbildung 1: Aufbau.",
	     "prov": [{"page_no": 1}]}
	  ],
	  "pictures": [
	    {"label": "picture", "content_layer": "body", "prov": [{"page_no": 1}],
	     "captions": [{"$ref": "#/texts/0"}],
	     "annotations": [{"kind": "description", "text": "Ein Schema."}]}
	  ]
	}`
	items := itemsFromJSONContent([]byte(doc))

	want := []DocItem{
		{Text: "Abbildung 1: Aufbau.", Page: 1, Label: "caption"},
		{Page: 1, Label: "picture", Picture: &DocPicture{Description: "Ein Schema."}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("caption must appear once:\n got %v\nwant %v", spew(items), spew(want))
	}
}
