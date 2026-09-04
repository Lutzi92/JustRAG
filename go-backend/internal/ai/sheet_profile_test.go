package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestProfileTableRegion_HappyPath exercises the completion hook the way
// kg_extractor_test.go / plan_queries_test.go do: canned JSON body in,
// parsed SheetProfileProposal out, and the prompt actually carries the
// grid + file/sheet names (spot-checking BuildRequest-equivalent wiring
// without a network).
func TestProfileTableRegion_HappyPath(t *testing.T) {
	var capturedUser, capturedSys string
	r := newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, user, sys, _, _ string) (*CompletionResult, error) {
		capturedUser = user
		capturedSys = sys
		return &CompletionResult{Content: `{"kind":"table","header_rows":[2],"confidence":0.92,"columns":[{"index":0,"name":"Lieferantennummer","role":"id","description":"SAP-Lieferantennummer"}]}`}, nil
	})

	req := SheetProfileRequest{
		FileName:  "gebaeude.xlsx",
		SheetName: "Blatt1",
		RowOffset: 2,
		Grid:      [][]string{{"Lieferantennummer", "BGF"}, {"L-001", "1200"}},
		Heuristic: SheetProfileProposal{Kind: "table", Confidence: 0.4},
	}
	prop, err := ProfileTableRegion(context.Background(), r, req, "kb1", "de", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.Kind != "table" || prop.Confidence != 0.92 {
		t.Errorf("prop = %+v", prop)
	}
	if len(prop.HeaderRows) != 1 || prop.HeaderRows[0] != 2 {
		t.Errorf("header_rows = %+v", prop.HeaderRows)
	}
	if len(prop.Columns) != 1 || prop.Columns[0].Role != "id" || prop.Columns[0].Description != "SAP-Lieferantennummer" {
		t.Errorf("columns = %+v", prop.Columns)
	}

	if !strings.Contains(capturedUser, "Lieferantennummer") {
		t.Errorf("user prompt missing grid content: %q", capturedUser)
	}
	if !strings.Contains(capturedUser, "gebaeude.xlsx") {
		t.Errorf("user prompt missing file name: %q", capturedUser)
	}
	if !strings.Contains(capturedUser, "Blatt1") {
		t.Errorf("user prompt missing sheet name: %q", capturedUser)
	}
	if capturedSys == "" {
		t.Errorf("system prompt must not be empty")
	}
}

// TestProfileTableRegion_JSONPreambleTolerated mirrors the tolerant-parse
// coverage every other structured-outputs caller in this package carries
// (kg_extractor, plan_queries): a markdown-fenced or prose-wrapped JSON
// body must still parse.
func TestProfileTableRegion_JSONPreambleTolerated(t *testing.T) {
	r := stubCompletion(t, "```json\n{\"kind\":\"form\",\"header_rows\":[0],\"confidence\":0.5,\"columns\":[]}\n```\n")
	prop, err := ProfileTableRegion(context.Background(), r, SheetProfileRequest{FileName: "f.xlsx", SheetName: "S"}, "kb1", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.Kind != "form" {
		t.Errorf("prop = %+v", prop)
	}
}

func TestProfileTableRegion_MalformedJSONReturnsError(t *testing.T) {
	r := stubCompletion(t, "this is not json at all")
	_, err := ProfileTableRegion(context.Background(), r, SheetProfileRequest{FileName: "f.xlsx", SheetName: "S"}, "kb1", "en", "")
	if err == nil {
		t.Fatal("expected a parse error for malformed JSON")
	}
}

func TestProfileTableRegion_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	_, err := ProfileTableRegion(context.Background(), r, SheetProfileRequest{FileName: "f.xlsx", SheetName: "S"}, "kb1", "en", "")
	if err == nil {
		t.Fatal("expected the completion error to bubble")
	}
}

func TestProfileTableRegion_EmptyCompletionReturnsError(t *testing.T) {
	r := stubCompletion(t, "   ")
	_, err := ProfileTableRegion(context.Background(), r, SheetProfileRequest{FileName: "f.xlsx", SheetName: "S"}, "kb1", "en", "")
	if err == nil {
		t.Fatal("expected an error for an empty completion body")
	}
}
