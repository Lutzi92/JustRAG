package app

import (
	"context"
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
	if c.Options.TableMode != "fast" {
		t.Errorf("table mode = %q, want fast default", c.Options.TableMode)
	}
}
