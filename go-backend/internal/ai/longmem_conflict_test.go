package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestClassifyLongmemConflict_EmptyCandidatesShortCircuits: no LLM
// call should fire when there's nothing to compare against. The
// verdict must be CreateNew so the caller's plain Insert path runs.
func TestClassifyLongmemConflict_EmptyCandidatesShortCircuits(t *testing.T) {
	t.Parallel()
	// Resolver with nil server: any LLM call would panic / error,
	// so the short-circuit absence-of-call is what we're asserting.
	resolver := &ConfigResolver{}
	v, err := ClassifyLongmemConflict(
		context.Background(),
		resolver,
		LongmemCandidate{Kind: "semantic", Content: "fact"},
		nil,
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("empty candidates: expected nil error, got %v", err)
	}
	if v.Action != LongmemConflictCreateNew {
		t.Errorf("empty candidates: want CreateNew, got %q", v.Action)
	}
	if len(v.SupersedeIDs) != 0 {
		t.Errorf("empty candidates: SupersedeIDs must be empty, got %v", v.SupersedeIDs)
	}
}

// TestClassifyLongmemConflict_Supersede happy path: LLM emits a
// JSON object naming supersede_ids, parser surfaces them and
// promotes the action.
func TestClassifyLongmemConflict_Supersede(t *testing.T) {
	resp := `{"action":"supersede","supersede_ids":[42]}`
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(resp, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")

	candidates := []LongmemCandidate{
		{ID: 42, Kind: "semantic", Content: "user works on project A"},
	}
	v, err := ClassifyLongmemConflict(
		context.Background(),
		resolver,
		LongmemCandidate{Kind: "semantic", Content: "user now works on project B"},
		candidates,
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictSupersede {
		t.Errorf("want Supersede, got %q", v.Action)
	}
	if len(v.SupersedeIDs) != 1 || v.SupersedeIDs[0] != 42 {
		t.Errorf("want SupersedeIDs=[42], got %v", v.SupersedeIDs)
	}
}

// TestClassifyLongmemConflict_SkipRedundant: identical facts → skip.
// SupersedeIDs in the LLM output must be ignored under skip_redundant.
func TestClassifyLongmemConflict_SkipRedundant(t *testing.T) {
	resp := `{"action":"skip_redundant","supersede_ids":[99]}` // stray IDs
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(resp, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")

	v, err := ClassifyLongmemConflict(
		context.Background(),
		resolver,
		LongmemCandidate{Kind: "semantic", Content: "user works on project A"},
		[]LongmemCandidate{{ID: 99, Kind: "semantic", Content: "user works on project A"}},
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictSkipRedundant {
		t.Errorf("want SkipRedundant, got %q", v.Action)
	}
	if len(v.SupersedeIDs) != 0 {
		t.Errorf("SkipRedundant must clear SupersedeIDs, got %v", v.SupersedeIDs)
	}
}

// TestParseLongmemConflictVerdict_EmptyActionFallsBackToCreateNew:
// safety contract — an unrecognised action string must NOT pass
// through. We default to CreateNew so no fact is silently dropped
// or wrongly used to overwrite an existing one.
func TestParseLongmemConflictVerdict_EmptyActionFallsBackToCreateNew(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"action":""}`,
		`{"action":"unknown_action"}`,
		`{"action":"DELETE_ALL"}`, // anything outside the closed enum
	}
	for _, in := range cases {
		v, err := parseLongmemConflictVerdict(in)
		if err != nil {
			t.Errorf("parse %q: unexpected error %v", in, err)
			continue
		}
		if v.Action != LongmemConflictCreateNew {
			t.Errorf("parse %q: want CreateNew, got %q", in, v.Action)
		}
		if len(v.SupersedeIDs) != 0 {
			t.Errorf("parse %q: unknown action must clear SupersedeIDs, got %v", in, v.SupersedeIDs)
		}
	}
}

// TestParseLongmemConflictVerdict_SupersedeWithoutIDsDowngradesToCreateNew:
// a supersede verdict without any IDs is incoherent. We downgrade
// rather than silently doing nothing, so the new fact still lands.
func TestParseLongmemConflictVerdict_SupersedeWithoutIDsDowngradesToCreateNew(t *testing.T) {
	t.Parallel()
	v, err := parseLongmemConflictVerdict(`{"action":"supersede"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictCreateNew {
		t.Errorf("empty supersede_ids: want CreateNew, got %q", v.Action)
	}
}

// TestParseLongmemConflictVerdict_AcceptsSingularSupersedeID: defensive
// fallback for models that emit the singular form. Should fold into
// SupersedeIDs as a single-element list.
func TestParseLongmemConflictVerdict_AcceptsSingularSupersedeID(t *testing.T) {
	t.Parallel()
	v, err := parseLongmemConflictVerdict(`{"action":"supersede","supersede_id":7}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictSupersede {
		t.Errorf("want Supersede, got %q", v.Action)
	}
	if len(v.SupersedeIDs) != 1 || v.SupersedeIDs[0] != 7 {
		t.Errorf("singular supersede_id should fold into SupersedeIDs=[7], got %v", v.SupersedeIDs)
	}
}

// TestParseLongmemConflictVerdict_TolerantOfProseWrapping: real
// LLMs sometimes prepend an explanation before the JSON. The
// parser must find the JSON object and recover.
func TestParseLongmemConflictVerdict_TolerantOfProseWrapping(t *testing.T) {
	t.Parallel()
	v, err := parseLongmemConflictVerdict(`Here is the verdict:
{"action":"skip_redundant"}
Hope that helps!`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictSkipRedundant {
		t.Errorf("want SkipRedundant, got %q", v.Action)
	}
}

// TestClassifyLongmemConflict_SendsStructuredPayload pins the request
// shape: the user message must be a JSON object with new_fact and
// existing_facts so the prompt's parsing instructions land on the
// right input. Without this pin, a future refactor that flattened
// the payload to plain text would silently break the prompt.
func TestClassifyLongmemConflict_SendsStructuredPayload(t *testing.T) {
	var captured string
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = string(body)
		writeChatJSON(w, chatResponse(`{"action":"create_new"}`, ""))
	})
	resolver := resolverForServer(srv, "gpt-4o")

	_, err := ClassifyLongmemConflict(
		context.Background(),
		resolver,
		LongmemCandidate{Kind: "semantic", Content: "new fact"},
		[]LongmemCandidate{{ID: 1, Kind: "semantic", Content: "old fact"}},
		"", "en", "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The payload sits as a JSON-encoded STRING inside the
	// chat-completion request body, so the inner JSON's quotes are
	// escaped (`\"`). The fragments below match the escaped form.
	mustContain := []string{`\"new_fact\"`, `\"existing_facts\"`, `\"id\":1`}
	for _, frag := range mustContain {
		if !strings.Contains(captured, frag) {
			t.Errorf("payload missing fragment %q in body: %s", frag, captured)
		}
	}
}
