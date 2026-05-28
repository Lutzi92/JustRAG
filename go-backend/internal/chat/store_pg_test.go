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

	if len(got.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(got.Sources))
	}
	if got.Sources[0].FileID != "f1" || got.Sources[0].Score != 0.9 {
		t.Errorf("unexpected source: %+v", got.Sources[0])
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
