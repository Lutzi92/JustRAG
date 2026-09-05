package app

import (
	"context"
	"strings"
	"testing"
)

type fakeDoclingSCR struct{ vals map[string]string }

func (f fakeDoclingSCR) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := f.vals[k]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestBuildDoclingClient_PopulatesCaptionOptions(t *testing.T) {
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_enabled":                     "true",
		"docling_base_url":                    "http://docling:5001",
		"docling_picture_description_enabled": "true",
		"docling_picture_area_threshold":      "0.1",
		"docling_table_mode":                  "accurate",
		"describe_image_model":                "jlu/gemma-4-26b-it",
	}}
	// nil resolver: model still resolves from site-config; the endpoint+key
	// (which need a live AI provider config) are left empty.
	c := buildDoclingClient(context.Background(), scr, nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if !c.Options.PictureDescription {
		t.Error("PictureDescription should be true")
	}
	if c.Options.PictureAreaThreshold != 0.1 {
		t.Errorf("area threshold = %v, want 0.1", c.Options.PictureAreaThreshold)
	}
	if !c.Options.PictureClassification {
		t.Error("PictureClassification should default on when description is on")
	}
	if c.Options.TableMode != "accurate" {
		t.Errorf("table mode = %q, want accurate", c.Options.TableMode)
	}
	if c.Options.PictureAPIModel != "jlu/gemma-4-26b-it" {
		t.Errorf("vision model = %q, want jlu/gemma-4-26b-it (from describe_image_model)", c.Options.PictureAPIModel)
	}
}

func TestBuildDoclingClient_DefaultsWhenCaptionKeysUnset(t *testing.T) {
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_enabled":  "true",
		"docling_base_url": "http://docling:5001",
	}}
	c := buildDoclingClient(context.Background(), scr, nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.Options.PictureDescription {
		t.Error("PictureDescription should default off")
	}
	if c.Options.TableMode != "accurate" {
		t.Errorf("table mode = %q, want accurate default", c.Options.TableMode)
	}
}

func TestReadDoclingOptions_DefaultsForAGermanCorpus(t *testing.T) {
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_enabled":                     "true",
		"docling_base_url":                    "http://docling:5001",
		"docling_picture_description_enabled": "true",
	}}
	o := readDoclingOptions(context.Background(), scr, nil)
	if o.TableMode != "accurate" {
		t.Errorf("table mode = %q, want accurate (the sidecar's own default; \"fast\" was a downgrade)", o.TableMode)
	}
	if got := o.OCRLanguages; len(got) != 2 || got[0] != "de" || got[1] != "en" {
		t.Errorf("ocr languages = %v, want [de en]", got)
	}
	if o.ForceOCR {
		t.Error("force OCR must default off")
	}
	if o.DocumentTimeoutSeconds != 600 {
		t.Errorf("document timeout = %v, want 600", o.DocumentTimeoutSeconds)
	}
	if o.PictureTimeoutSeconds != 120 {
		t.Errorf("picture timeout = %v, want 120 (docling's 20 s default silently drops slow captions)", o.PictureTimeoutSeconds)
	}
	if !strings.Contains(o.PicturePrompt, "Sprache des Dokuments") || !strings.Contains(o.PicturePrompt, "Zahlen") {
		t.Errorf("default prompt must ask for the document's language and the figure's numbers, got %q", o.PicturePrompt)
	}
	// Charts come back as a markdown table of the read-off values — verified
	// live on testdata/figure-2p.pdf, where gemma-4 returned every bar.
	if !strings.Contains(o.PicturePrompt, "Markdown-Tabelle") {
		t.Errorf("default prompt must ask for chart data as a markdown table, got %q", o.PicturePrompt)
	}
}

func TestReadDoclingOptions_ReadsTheNewKeys(t *testing.T) {
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_picture_description_enabled":         "true",
		"docling_picture_description_prompt":          "Describe it.",
		"docling_picture_description_timeout_seconds": "45",
		"docling_ocr_languages":                       " de, en ,fr,",
		"docling_force_ocr":                           "true",
		"docling_document_timeout_seconds":            "900",
		"docling_table_mode":                          "fast",
	}}
	o := readDoclingOptions(context.Background(), scr, nil)
	if o.PicturePrompt != "Describe it." || o.PictureTimeoutSeconds != 45 {
		t.Errorf("prompt/timeout = %q/%v", o.PicturePrompt, o.PictureTimeoutSeconds)
	}
	if got := o.OCRLanguages; len(got) != 3 || got[0] != "de" || got[1] != "en" || got[2] != "fr" {
		t.Errorf("ocr languages = %v, want [de en fr]", got)
	}
	if !o.ForceOCR || o.DocumentTimeoutSeconds != 900 || o.TableMode != "fast" {
		t.Errorf("force/timeout/table = %v/%v/%q", o.ForceOCR, o.DocumentTimeoutSeconds, o.TableMode)
	}
}

func TestBuildDoclingClient_ResolvesOptionsPerRequest(t *testing.T) {
	// The admin panel edits site_config; nothing restarts the worker. The
	// client therefore re-reads its options on every convert.
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_enabled":    "true",
		"docling_base_url":   "http://docling:5001",
		"docling_table_mode": "fast",
	}}
	c := buildDoclingClient(context.Background(), scr, nil)
	if c == nil || c.OptionsFunc == nil {
		t.Fatal("client must carry an OptionsFunc")
	}
	scr.vals["docling_table_mode"] = "accurate"
	if got := c.OptionsFunc(context.Background()).TableMode; got != "accurate" {
		t.Errorf("table mode after site-config change = %q, want accurate", got)
	}
}

func TestBuildDoclingClient_UsesTheAsyncEndpoints(t *testing.T) {
	// The sync endpoint is capped by DOCLING_SERVE_MAX_SYNC_WAIT on the
	// sidecar; the worker must not depend on that setting being raised.
	scr := fakeDoclingSCR{vals: map[string]string{
		"docling_enabled":  "true",
		"docling_base_url": "http://docling:5001",
	}}
	c := buildDoclingClient(context.Background(), scr, nil)
	if c == nil || !c.Async {
		t.Fatal("worker client must use the async convert endpoints")
	}
}
