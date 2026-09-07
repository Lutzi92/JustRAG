package main

import (
	"context"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/vector"
)

// stubSearcher returns a fixed chunk so the adapter's field mapping is the
// only thing under test.
type stubSearcher struct{ chunk vector.SearchChunk }

func (s *stubSearcher) Search(context.Context, string, string, int, vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{Chunks: []vector.SearchChunk{s.chunk}}, nil
}

func (s *stubSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

// TestLegacySearchAdapterPropagatesFileName guards the retrieval-only eval
// path against silently dropping FileName. Goldens authored with
// must_cite_file_names (the re-ingest-resilient form — file UUIDs are
// regenerated on delete + re-upload) match on RetrievedChunk.FileName; when
// the adapter leaves it empty every such question scores recall 0.000 with
// no error, which reads as a retrieval regression rather than a harness bug.
func TestLegacySearchAdapterPropagatesFileName(t *testing.T) {
	a := &legacySearchAdapter{svc: &stubSearcher{chunk: vector.SearchChunk{
		FileID:   "file-uuid",
		FileName: "Stud.IP-Update.md",
		Score:    0.9,
	}}}

	got, err := a.Search(context.Background(), eval.Question{ID: "Q1", KbID: "kb"}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0].FileName != "Stud.IP-Update.md" {
		t.Errorf("FileName = %q, want %q", got[0].FileName, "Stud.IP-Update.md")
	}
	if got[0].FileID != "file-uuid" {
		t.Errorf("FileID = %q, want %q", got[0].FileID, "file-uuid")
	}
}

// stubChatSiteCfg is the inner reader the chat overlay wraps: it stands in
// for the live site_configs table.
type stubChatSiteCfg struct{ values map[string]string }

func (s *stubChatSiteCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := s.values[key]; ok {
		return &v, nil
	}
	return nil, nil
}

// TestChatOverlayReader_LongContextEnabledOverride asserts the --longcontext
// on|off overlay: the overlaid key is served from the run's map and every
// other key still delegates to the live reader. Without the delegation arm
// the overlay would blank the whole chat config for the run (every unset key
// reads as "unset"), which silently disables CRAG, the date line and the
// tabular router — a wrong A/B rather than a crash.
// Mutation A: returning the overlay for every key fails the delegation
// assertions. Mutation B: dropping chat_longcontext_enabled from the overlay
// map fails the first assertion.
func TestChatOverlayReader_LongContextEnabledOverride(t *testing.T) {
	inner := &stubChatSiteCfg{values: map[string]string{
		"chat_longcontext_enabled": "false",
		"crag_enabled":             "true",
	}}
	w := &chatOverlayReader{
		inner: inner,
		overlays: map[string]string{
			"chat_longcontext_enabled": "true",
			"chat_longcontext_mode":    "map_reduce",
		},
	}
	ctx := context.Background()

	got, err := w.GetSiteConfigValue(ctx, "chat_longcontext_enabled")
	if err != nil {
		t.Fatalf("GetSiteConfigValue: %v", err)
	}
	if got == nil || *got != "true" {
		t.Fatalf("chat_longcontext_enabled = %v, want overlay value \"true\"", got)
	}

	got, err = w.GetSiteConfigValue(ctx, "chat_longcontext_mode")
	if err != nil {
		t.Fatalf("GetSiteConfigValue: %v", err)
	}
	if got == nil || *got != "map_reduce" {
		t.Fatalf("chat_longcontext_mode = %v, want overlay value \"map_reduce\"", got)
	}

	// Delegation: a key outside the overlay must still read live.
	got, err = w.GetSiteConfigValue(ctx, "crag_enabled")
	if err != nil {
		t.Fatalf("GetSiteConfigValue: %v", err)
	}
	if got == nil || *got != "true" {
		t.Fatalf("crag_enabled = %v, want live value \"true\"", got)
	}

	// A key neither overlaid nor set stays unset (nil), not "".
	got, err = w.GetSiteConfigValue(ctx, "chat_supervisor_enabled")
	if err != nil {
		t.Fatalf("GetSiteConfigValue: %v", err)
	}
	if got != nil {
		t.Fatalf("chat_supervisor_enabled = %q, want nil (unset)", *got)
	}
}

// TestBuildChatOverlays_EmptyFlagsLeaveOverlayEmpty asserts that an unset
// --longcontext / --longcontext-mode adds nothing to the overlay, so a run
// without those flags reads the live site_config exactly as before the flags
// existed. Mutation: writing "" or "false" into the map for an empty flag
// fails this test (an empty overlay entry would pin the key to the zero
// value instead of delegating).
func TestBuildChatOverlays_EmptyFlagsLeaveOverlayEmpty(t *testing.T) {
	if got := buildChatOverlays("", "", "", "", nil); len(got) != 0 {
		t.Fatalf("buildChatOverlays(\"\", \"\", \"\", \"\", nil) = %v, want empty map", got)
	}
	got := buildChatOverlays("on", "", "", "", nil)
	if len(got) != 1 || got["chat_longcontext_enabled"] != "true" {
		t.Fatalf("buildChatOverlays(\"on\", \"\", \"\", \"\", nil) = %v, want only chat_longcontext_enabled=true", got)
	}
	got = buildChatOverlays("off", "flat", "", "", nil)
	if got["chat_longcontext_enabled"] != "false" || got["chat_longcontext_mode"] != "flat" {
		t.Fatalf("buildChatOverlays(\"off\", \"flat\", \"\", \"\", nil) = %v", got)
	}
}

// --conflict-surfacing overlays chat_conflict_surfacing_enabled for one run
// (W5-R7 measurement, no site_configs mutation), composes with the
// long-context flags, and — like them — contributes NO entry when empty so
// the live site_config still decides.
//
// Mutation: dropping the conflict arm from buildChatOverlays (so "on"
// produces an empty overlay) fails this test.
func TestBuildChatOverlays_ConflictSurfacing(t *testing.T) {
	got := buildChatOverlays("", "", "on", "", nil)
	if len(got) != 1 || got["chat_conflict_surfacing_enabled"] != "true" {
		t.Fatalf("buildChatOverlays(\"\", \"\", \"on\", \"\", nil) = %v, want only chat_conflict_surfacing_enabled=true", got)
	}
	got = buildChatOverlays("", "", "off", "", nil)
	if len(got) != 1 || got["chat_conflict_surfacing_enabled"] != "false" {
		t.Fatalf("buildChatOverlays(\"\", \"\", \"off\", \"\", nil) = %v, want only chat_conflict_surfacing_enabled=false", got)
	}
	// An unrecognised value contributes nothing (the CLI rejects it before
	// this point; the map must not invent a value either way).
	if got := buildChatOverlays("", "", "maybe", "", nil); len(got) != 0 {
		t.Fatalf("buildChatOverlays(\"\", \"\", \"maybe\", \"\", nil) = %v, want empty map", got)
	}
	got = buildChatOverlays("on", "flat", "on", "", nil)
	if len(got) != 3 ||
		got["chat_longcontext_enabled"] != "true" ||
		got["chat_longcontext_mode"] != "flat" ||
		got["chat_conflict_surfacing_enabled"] != "true" {
		t.Fatalf("buildChatOverlays(\"on\", \"flat\", \"on\", \"\", nil) = %v, want all three keys", got)
	}
}

// TestParseChatOverlayFlags_ParseOK covers the W6-R18 happy path: one or
// more well-formed "key=value" pairs parse into a map with the exact keys
// and values given, and a value itself containing '=' (e.g. a JSON blob)
// stays intact because strings.Cut only ever splits at the FIRST '='.
func TestParseChatOverlayFlags_ParseOK(t *testing.T) {
	got, err := parseChatOverlayFlags(nil)
	if err != nil || got != nil {
		t.Fatalf("parseChatOverlayFlags(nil) = (%v, %v), want (nil, nil)", got, err)
	}
	got, err = parseChatOverlayFlags([]string{"chat_conflict_max_chunks=30"})
	if err != nil {
		t.Fatalf("parseChatOverlayFlags single pair: unexpected error %v", err)
	}
	if len(got) != 1 || got["chat_conflict_max_chunks"] != "30" {
		t.Fatalf("parseChatOverlayFlags single pair = %v, want {chat_conflict_max_chunks: 30}", got)
	}
	got, err = parseChatOverlayFlags([]string{"a=1", "b=2==3"})
	if err != nil {
		t.Fatalf("parseChatOverlayFlags two pairs: unexpected error %v", err)
	}
	if len(got) != 2 || got["a"] != "1" || got["b"] != "2==3" {
		t.Fatalf("parseChatOverlayFlags two pairs = %v, want {a:1, b:2==3} (value keeps every '=' after the first)", got)
	}
}

// TestParseChatOverlayFlags_Malformed covers the two rejected shapes: no
// '=' at all, and an empty key before the '='. Both must return a non-nil
// error (main() turns that into exit 2) rather than silently dropping the
// pair or inventing a key.
func TestParseChatOverlayFlags_Malformed(t *testing.T) {
	if _, err := parseChatOverlayFlags([]string{"no_equals_sign"}); err == nil {
		t.Fatal("parseChatOverlayFlags(\"no_equals_sign\") = nil error, want an error (missing '=')")
	}
	if _, err := parseChatOverlayFlags([]string{"=value_only"}); err == nil {
		t.Fatal("parseChatOverlayFlags(\"=value_only\") = nil error, want an error (empty key)")
	}
	// One good pair ahead of a bad one must still error — the whole flag
	// set is validated before any of it is used, not applied partially.
	if _, err := parseChatOverlayFlags([]string{"good=1", "bad"}); err == nil {
		t.Fatal("parseChatOverlayFlags([\"good=1\", \"bad\"]) = nil error, want an error")
	}
}

// TestBuildChatOverlays_ExtraPrecedence pins the W6-R18 precedence rule
// documented on buildChatOverlays: a --chat-overlay entry for a key one of
// the three named flags ALSO sets wins, because extra is merged in last.
// Mutation: merging extra BEFORE the named-flag switches (or merging it at
// all) in the wrong order fails this test.
func TestBuildChatOverlays_ExtraPrecedence(t *testing.T) {
	got := buildChatOverlays("", "", "on", "", map[string]string{"chat_conflict_surfacing_enabled": "false"})
	if len(got) != 1 || got["chat_conflict_surfacing_enabled"] != "false" {
		t.Fatalf("extra should win over --conflict-surfacing on: got %v, want {chat_conflict_surfacing_enabled: false}", got)
	}
	// A --chat-overlay key that does not collide with any named flag is
	// simply added alongside them.
	got = buildChatOverlays("on", "", "", "", map[string]string{"chat_conflict_max_chunks": "30"})
	if len(got) != 2 || got["chat_longcontext_enabled"] != "true" || got["chat_conflict_max_chunks"] != "30" {
		t.Fatalf("non-colliding extra key should be added alongside the named flag: got %v", got)
	}
}

// ---------------------------------------------------------------------------
// W6-R6: --policy
// ---------------------------------------------------------------------------

const policyOneRule = `[{"when":{"query_type":["complex_reasoning"]},"orchestrator":"supervisor","mode":"force"}]`

// A non-empty --policy lands verbatim on chat_orchestrator_policy; an empty
// one contributes no entry at all (so the live site_config is read).
// Mutation: dropping the policy arm makes the first assertion fail; writing
// the key unconditionally makes the second fail.
func TestBuildChatOverlays_Policy(t *testing.T) {
	got := buildChatOverlays("", "", "", policyOneRule, nil)
	if len(got) != 1 || got["chat_orchestrator_policy"] != policyOneRule {
		t.Fatalf("buildChatOverlays with --policy = %v, want only chat_orchestrator_policy", got)
	}
	if got := buildChatOverlays("", "", "", "", nil); len(got) != 0 {
		t.Fatalf("buildChatOverlays with an empty --policy = %v, want empty map", got)
	}
	// Whitespace-only is "unset", the same reading chatpolicy's parser has.
	if got := buildChatOverlays("", "", "", "   ", nil); len(got) != 0 {
		t.Fatalf("buildChatOverlays with a whitespace --policy = %v, want empty map", got)
	}
	// Composes with the other named chat overlays.
	got = buildChatOverlays("on", "", "on", policyOneRule, nil)
	if len(got) != 3 || got["chat_orchestrator_policy"] != policyOneRule ||
		got["chat_longcontext_enabled"] != "true" || got["chat_conflict_surfacing_enabled"] != "true" {
		t.Fatalf("buildChatOverlays with --policy + two named flags = %v, want all three keys", got)
	}
}

// validatePolicyFlag runs the real chatpolicy validator, so an invalid
// document is a usage error before the run starts rather than a silently
// ignored flag or a mid-run reader warning.
func TestValidatePolicyFlag(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		extra   map[string]string
		wantErr string // substring; "" = expect success
	}{
		{name: "empty is valid", policy: ""},
		{name: "whitespace is valid", policy: "  \n\t "},
		{name: "empty array is valid", policy: "[]"},
		{name: "one good rule", policy: policyOneRule},
		{name: "not an array", policy: `{"when":{}}`, wantErr: "must be a JSON array"},
		{name: "malformed json", policy: `[{"orchestrator":`, wantErr: "invalid policy JSON"},
		{name: "trailing data", policy: `[] oops`, wantErr: "trailing data"},
		{name: "unknown orchestrator", policy: `[{"when":{},"orchestrator":"nope","mode":"force"}]`, wantErr: "unknown orchestrator"},
		{name: "unknown mode", policy: `[{"when":{},"orchestrator":"agentic","mode":"maybe"}]`, wantErr: "unknown mode"},
		{name: "unknown when key", policy: `[{"when":{"nope":true},"orchestrator":"agentic","mode":"force"}]`, wantErr: "unknown field"},
		{name: "unknown query_type", policy: `[{"when":{"query_type":["chitchat"]},"orchestrator":"agentic","mode":"force"}]`, wantErr: "unknown query_type"},
		{
			name:    "collides with --chat-overlay",
			policy:  policyOneRule,
			extra:   map[string]string{"chat_orchestrator_policy": "[]"},
			wantErr: "conflicts with --policy",
		},
		{
			// The collision is rejected even when --policy itself is empty:
			// an unvalidated document must not reach the run through the
			// generic escape hatch either.
			name:    "chat-overlay alone still collides",
			policy:  "",
			extra:   map[string]string{"chat_orchestrator_policy": "garbage"},
			wantErr: "conflicts with --policy",
		},
		{
			name:  "unrelated chat-overlay key is fine",
			extra: map[string]string{"chat_conflict_max_chunks": "30"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePolicyFlag(tt.policy, tt.extra)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePolicyFlag(%q) = %v, want nil", tt.policy, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validatePolicyFlag(%q) = nil, want an error containing %q", tt.policy, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validatePolicyFlag(%q) = %q, want it to contain %q", tt.policy, err, tt.wantErr)
			}
		})
	}
}
