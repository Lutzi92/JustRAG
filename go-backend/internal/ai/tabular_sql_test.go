package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerateTabularSQL_HappyPath(t *testing.T) {
	r := stubCompletion(t, `{"sql":"SELECT 1","rationale":"r","confidence":0.9}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "how many?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.SQL == nil || *prop.SQL != "SELECT 1" {
		t.Errorf("SQL = %+v", prop.SQL)
	}
	if prop.Rationale != "r" {
		t.Errorf("Rationale = %q", prop.Rationale)
	}
	if prop.Confidence != 0.9 {
		t.Errorf("Confidence = %v", prop.Confidence)
	}
}

func TestGenerateTabularSQL_NullSQL(t *testing.T) {
	r := stubCompletion(t, `{"sql":null,"rationale":"cannot answer","confidence":0}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.SQL != nil {
		t.Errorf("expected nil SQL, got %+v", *prop.SQL)
	}
}

func TestGenerateTabularSQL_MalformedJSONReturnsError(t *testing.T) {
	r := stubCompletion(t, "this is not json at all")
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	_, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err == nil {
		t.Fatal("expected a parse error for malformed JSON, got nil (empty-proposal fallback is not allowed here)")
	}
}

func TestGenerateTabularSQL_EmptyCompletionReturnsError(t *testing.T) {
	r := stubCompletion(t, "   ")
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	_, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err == nil {
		t.Fatal("expected an error for an empty completion body")
	}
}

func TestGenerateTabularSQL_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	_, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err == nil {
		t.Fatal("expected the completion error to bubble")
	}
}

func TestGenerateTabularSQL_ConfidenceClampedHigh(t *testing.T) {
	r := stubCompletion(t, `{"sql":"SELECT 1","rationale":"r","confidence":1.7}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", prop.Confidence)
	}
}

func TestGenerateTabularSQL_ConfidenceClampedLow(t *testing.T) {
	r := stubCompletion(t, `{"sql":"SELECT 1","rationale":"r","confidence":-0.3}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.Confidence != 0.0 {
		t.Errorf("Confidence = %v, want 0.0", prop.Confidence)
	}
}

func TestGenerateTabularSQL_PromptCarriesSchemaAndQuestion(t *testing.T) {
	var capturedUser, capturedSys string
	r := newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, user, sys, _, _ string) (*CompletionResult, error) {
		capturedUser = user
		capturedSys = sys
		return &CompletionResult{Content: `{"sql":"SELECT 1","rationale":"r","confidence":0.5}`}, nil
	})
	req := TabularSQLRequest{
		Lang:       "en",
		TodayISO:   "2026-09-04",
		SchemaText: "tabular.gebaeudedaten(\"id\" text, \"bgf_m2\" numeric)",
		Question:   "What is the total BGF?",
	}
	if _, err := GenerateTabularSQL(context.Background(), r, req, "kb1", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(capturedUser, "tabular.gebaeudedaten") {
		t.Errorf("user prompt missing schema text: %q", capturedUser)
	}
	if !strings.Contains(capturedUser, "What is the total BGF?") {
		t.Errorf("user prompt missing question: %q", capturedUser)
	}
	if capturedSys == "" {
		t.Errorf("system prompt must not be empty")
	}
}

func TestGenerateTabularSQL_RepairRoundIncludesPreviousAttempt(t *testing.T) {
	var capturedUser string
	r := newTestResolverWithCompletion(t, func(_ context.Context, _ *ConfigResolver, user, _, _, _ string) (*CompletionResult, error) {
		capturedUser = user
		return &CompletionResult{Content: `{"sql":"SELECT 2","rationale":"r","confidence":0.5}`}, nil
	})
	req := TabularSQLRequest{
		Lang:        "en",
		TodayISO:    "2026-09-04",
		SchemaText:  "tabular.foo",
		Question:    "q?",
		PreviousSQL: "SELECT bogus FROM tabular.foo",
		Failure:     "column \"bogus\" does not exist",
	}
	if _, err := GenerateTabularSQL(context.Background(), r, req, "kb1", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(capturedUser, "SELECT bogus FROM tabular.foo") {
		t.Errorf("repair prompt missing previous SQL: %q", capturedUser)
	}
	if !strings.Contains(capturedUser, "does not exist") {
		t.Errorf("repair prompt missing failure text: %q", capturedUser)
	}
}

func TestGenerateTabularSQL_SchemaNameIsTabularSQL(t *testing.T) {
	if tabularSQLSpec.Name != "tabular_sql" {
		t.Errorf("spec name = %q, want tabular_sql", tabularSQLSpec.Name)
	}
}

func TestGenerateTabularSQL_TimeoutBoundedAt10s(t *testing.T) {
	var deadlineOK bool
	r := newTestResolverWithCompletion(t, func(ctx context.Context, _ *ConfigResolver, _, _, _, _ string) (*CompletionResult, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected a context deadline")
		}
		deadlineOK = time.Until(dl) <= 10*time.Second+time.Second // small slack for test wall-clock
		return &CompletionResult{Content: `{"sql":"SELECT 1","rationale":"r","confidence":0.5}`}, nil
	})
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	if _, err := GenerateTabularSQL(context.Background(), r, req, "kb1", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !deadlineOK {
		t.Errorf("context deadline exceeded the 10s budget")
	}
}

// TestGenerateTabularSQL_EmptySQLStringNormalizedToNil covers Fix round 1's
// Minor item: {"sql": ""} must behave exactly like {"sql": null} for
// callers, not leave them with a non-nil-but-useless SQL pointer.
func TestGenerateTabularSQL_EmptySQLStringNormalizedToNil(t *testing.T) {
	r := stubCompletion(t, `{"sql":"","rationale":"cannot answer","confidence":0}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.SQL != nil {
		t.Errorf("expected nil SQL for an empty string, got %+v", *prop.SQL)
	}
}

func TestGenerateTabularSQL_WhitespaceOnlySQLStringNormalizedToNil(t *testing.T) {
	r := stubCompletion(t, `{"sql":"   ","rationale":"cannot answer","confidence":0}`)
	req := TabularSQLRequest{Lang: "en", TodayISO: "2026-09-04", SchemaText: "tabular.foo", Question: "q?"}
	prop, err := GenerateTabularSQL(context.Background(), r, req, "kb1", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prop.SQL != nil {
		t.Errorf("expected nil SQL for a whitespace-only string, got %+v", *prop.SQL)
	}
}
