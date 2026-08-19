package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestPGStore_UpdateMessageVerification_NilIsNoop confirms that calling
// UpdateMessageVerification with a nil result returns nil without touching
// the database (the early-return guard makes no DB calls).
func TestPGStore_UpdateMessageVerification_NilIsNoop(t *testing.T) {
	s := &PGStore{pool: nil} // nil pool is fine — the function returns before using it
	err := s.UpdateMessageVerification(context.Background(), "some-id", (*MessageVerification)(nil))
	if err != nil {
		t.Errorf("expected nil error for nil verification, got: %v", err)
	}
}

func TestPGStore_UpdateMessageTraceID_EmptyIsNoop(t *testing.T) {
	s := &PGStore{} // pool unused on the empty-traceID branch
	if err := s.UpdateMessageTraceID(context.Background(), "any-id", ""); err != nil {
		t.Fatalf("expected nil error for empty traceID, got %v", err)
	}
}

// TestToMessageRow_DecodesJSONB verifies that the JSONB → typed-field
// conversion in toMessageRow produces concrete []ChatSource and
// *MessageVerification values, instead of leaving them as the previous
// untyped `any` (which masked schema drift).
func TestToMessageRow_DecodesJSONB(t *testing.T) {
	sourcesJSON := json.RawMessage(`[{"index":1,"fileName":"a.pdf","fileId":"f1","content":"hello","score":0.9,"pages":[1,2]}]`)
	verificationJSON := json.RawMessage(`{"verified":true,"score":85,"issues":["minor"],"citations":[{"n":1,"verified":true,"reason":"ok"}]}`)

	got, err := toMessageRow(messageDBRow{
		ID:           "m1",
		ChatID:       "c1",
		Role:         "ai",
		Content:      "hi",
		Sources:      sourcesJSON,
		Verification: verificationJSON,
		CreatedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("toMessageRow returned error: %v", err)
	}

	// Sources is handed on verbatim (see MessageRow.Sources); the typed decode
	// survives as a validation step, which TestToMessageRow_TypeDriftReturnsError
	// pins. DecodedSources() is the typed view for callers that need one.
	decoded := got.DecodedSources()
	if len(decoded) != 1 {
		t.Fatalf("expected 1 source, got %d", len(decoded))
	}
	if decoded[0].FileID != "f1" || decoded[0].Score != 0.9 {
		t.Errorf("unexpected source: %+v", decoded[0])
	}
	if string(got.Sources) != string(sourcesJSON) {
		t.Errorf("Sources must round-trip verbatim:\n got %s\nwant %s", got.Sources, sourcesJSON)
	}
	if got.Verification == nil {
		t.Fatal("expected verification to decode, got nil")
	}
	if !got.Verification.Verified || got.Verification.Score != 85 {
		t.Errorf("unexpected verification: %+v", got.Verification)
	}
}

// TestToMessageRow_NullJSONB confirms that NULL and JSON-null jsonb columns
// produce nil typed fields (not panics, not bogus zero structs).
func TestToMessageRow_NullJSONB(t *testing.T) {
	cases := []messageDBRow{
		{Sources: nil, Verification: nil},
		{Sources: json.RawMessage(`null`), Verification: json.RawMessage(`null`)},
		{Sources: json.RawMessage(``), Verification: json.RawMessage(``)},
	}
	for i, in := range cases {
		got, err := toMessageRow(in)
		if err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
			continue
		}
		if got.Sources != nil {
			t.Errorf("case %d: expected nil Sources, got %+v", i, got.Sources)
		}
		if got.Verification != nil {
			t.Errorf("case %d: expected nil Verification, got %+v", i, got.Verification)
		}
	}
}

// TestToMessageRow_CorruptJSONBReturnsError ensures that a row whose
// jsonb columns contain malformed bytes surfaces as an error rather than
// being silently downgraded to an empty Sources/Verification.
func TestToMessageRow_CorruptJSONBReturnsError(t *testing.T) {
	if _, err := toMessageRow(messageDBRow{
		ID:      "m1",
		Sources: json.RawMessage(`{not valid json`),
	}); err == nil {
		t.Error("expected error for corrupt sources jsonb, got nil")
	}
	if _, err := toMessageRow(messageDBRow{
		ID:           "m1",
		Verification: json.RawMessage(`{not valid json`),
	}); err == nil {
		t.Error("expected error for corrupt verification jsonb, got nil")
	}
}

// TestToMessageRow_PreservesResearchFindingShape pins the fix for a data-loss
// bug that predates the workspace rework: research sessions store their
// findings in messages.sources as [{content, sources, relevanceScore}], but
// toMessageRow used to decode that column into []ChatSource and hand the
// decoded value to the client. ChatSource has no `sources` and no
// `relevanceScore` field, so encoding/json dropped both — the frontend then
// crashed on `finding.sources.map(...)` ("e.sources is undefined") when a
// saved report was reopened from the history.
//
// The decode is deliberately KEPT as a validation step (see
// TestToMessageRow_DecodesJSONB's rationale: typed decoding surfaces schema
// drift that an untyped `any` masked), but what reaches the client is the
// stored JSON verbatim.
func TestToMessageRow_PreservesResearchFindingShape(t *testing.T) {
	findingsJSON := json.RawMessage(
		`[{"content":"Befund","sources":[{"fileId":"f1","fileName":"a.md"}],"relevanceScore":0.8}]`)

	got, err := toMessageRow(messageDBRow{
		ID: "m1", ChatID: "c1", Role: "ai", Content: "Bericht",
		Sources: findingsJSON, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("toMessageRow returned error: %v", err)
	}

	var findings []struct {
		Content string `json:"content"`
		Sources []struct {
			FileID string `json:"fileId"`
		} `json:"sources"`
		RelevanceScore float64 `json:"relevanceScore"`
	}
	if err := json.Unmarshal(got.Sources, &findings); err != nil {
		t.Fatalf("Sources is not valid JSON after conversion: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if len(findings[0].Sources) != 1 || findings[0].Sources[0].FileID != "f1" {
		t.Errorf("the finding's nested sources were dropped: %+v", findings[0])
	}
	if findings[0].RelevanceScore != 0.8 {
		t.Errorf("relevanceScore = %v, want 0.8 (dropped by the typed decode)", findings[0].RelevanceScore)
	}
}

// TestToMessageRow_TypeDriftReturnsError keeps the guarantee that motivated the
// typed decode in the first place: a column whose shape drifted (here a string
// where a number belongs) must error rather than reach the client. Passing the
// raw bytes through must not turn this into a silent success.
func TestToMessageRow_TypeDriftReturnsError(t *testing.T) {
	if _, err := toMessageRow(messageDBRow{
		ID:      "m1",
		Sources: json.RawMessage(`[{"index":1,"score":"high"}]`),
	}); err == nil {
		t.Error("expected error for type-drifted sources jsonb, got nil")
	}
}
