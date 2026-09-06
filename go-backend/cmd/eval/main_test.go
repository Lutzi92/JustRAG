package main

import (
	"context"
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
	if got := buildChatOverlays("", ""); len(got) != 0 {
		t.Fatalf("buildChatOverlays(\"\", \"\") = %v, want empty map", got)
	}
	got := buildChatOverlays("on", "")
	if len(got) != 1 || got["chat_longcontext_enabled"] != "true" {
		t.Fatalf("buildChatOverlays(\"on\", \"\") = %v, want only chat_longcontext_enabled=true", got)
	}
	got = buildChatOverlays("off", "flat")
	if got["chat_longcontext_enabled"] != "false" || got["chat_longcontext_mode"] != "flat" {
		t.Fatalf("buildChatOverlays(\"off\", \"flat\") = %v", got)
	}
}
