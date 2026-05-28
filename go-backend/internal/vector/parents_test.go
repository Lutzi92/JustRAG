package vector

import (
	"context"
	"testing"
)

func TestParentChunkInput_ZeroValueIsValid(t *testing.T) {
	t.Parallel()
	in := ParentChunkInput{
		KbID:    "00000000-0000-0000-0000-000000000001",
		FileID:  "00000000-0000-0000-0000-000000000002",
		Content: "parent content text",
	}
	if in.ContextualPrefix != "" {
		t.Errorf("ContextualPrefix default: got %q, want empty", in.ContextualPrefix)
	}
	if in.Metadata != nil {
		t.Errorf("Metadata default: got %v, want nil", in.Metadata)
	}
}

func TestParentChunkRow_FieldsMapToColumns(t *testing.T) {
	t.Parallel()
	row := ParentChunkRow{
		ID:               "11111111-1111-1111-1111-111111111111",
		Content:          "parent text",
		ContextualPrefix: "context: this passage is about X",
	}
	if row.ID == "" {
		t.Error("ID should be set")
	}
	if row.Content != "parent text" {
		t.Errorf("Content mismatch: %q", row.Content)
	}
	if row.ContextualPrefix == "" {
		t.Error("ContextualPrefix should be set when populated")
	}
}

func TestNullableUUIDString_NilReturnsNil(t *testing.T) {
	t.Parallel()
	if got := nullableUUIDString(nil); got != nil {
		t.Errorf("nil ptr: got %v, want nil", got)
	}
}

func TestNullableUUIDString_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	empty := ""
	if got := nullableUUIDString(&empty); got != nil {
		t.Errorf("empty string: got %v, want nil", got)
	}
}

func TestNullableUUIDString_NonEmptyReturnsValue(t *testing.T) {
	t.Parallel()
	id := "11111111-1111-1111-1111-111111111111"
	got := nullableUUIDString(&id)
	if s, ok := got.(string); !ok || s != id {
		t.Errorf("non-empty string: got %v (type %T), want %q", got, got, id)
	}
}

// fakeParentLookup satisfies the lookup contract for unit tests of
// callers that consume the parent-content map without touching the DB.
type fakeParentLookup struct {
	rows map[string]ParentChunkRow
	err  error
}

func (f *fakeParentLookup) LookupParentContent(_ context.Context, ids []string) (map[string]ParentChunkRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]ParentChunkRow, len(ids))
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func TestFakeParentLookup_ReturnsRequestedRows(t *testing.T) {
	t.Parallel()
	f := &fakeParentLookup{
		rows: map[string]ParentChunkRow{
			"a": {ID: "a", Content: "alpha"},
			"b": {ID: "b", Content: "beta"},
		},
	}
	got, err := f.LookupParentContent(context.Background(), []string{"a", "missing"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got["a"]; !ok {
		t.Error("expected 'a' in result")
	}
	if _, ok := got["missing"]; ok {
		t.Error("missing ID should not be in result")
	}
}

// ---------------------------------------------------------------------------
// swapToParents — dedup behaviour added 2026-05-05 to fix the parent-child
// eval regression (overall MRR -20pp on the JLU 89-question set when
// duplicate-parent rows filled the top-K).
// ---------------------------------------------------------------------------

func ptr(s string) *string { return &s }

func TestSwapToParents_FlatRowsPassThrough(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "1", Content: "alpha"},
		{ID: "2", Content: "beta"},
	}
	out, swapped, dropped := swapToParents(docs, map[string]ParentChunkRow{})
	if len(out) != 2 {
		t.Fatalf("flat rows: want 2 out, got %d", len(out))
	}
	if swapped != 0 || dropped != 0 {
		t.Errorf("flat rows: swapped=%d dropped=%d, want 0/0", swapped, dropped)
	}
	if out[0].Content != "alpha" || out[1].Content != "beta" {
		t.Errorf("flat rows: content mutated")
	}
}

func TestSwapToParents_SwapsContentForParentChildRows(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "child1", Content: "child text", ParentChunkID: ptr("parentA")},
	}
	parents := map[string]ParentChunkRow{
		"parentA": {ID: "parentA", Content: "PARENT TEXT", ContextualPrefix: "ctx"},
	}
	out, swapped, dropped := swapToParents(docs, parents)
	if swapped != 1 || dropped != 0 {
		t.Errorf("swapped=%d dropped=%d, want 1/0", swapped, dropped)
	}
	if out[0].Content != "PARENT TEXT" {
		t.Errorf("content not swapped: got %q", out[0].Content)
	}
	if out[0].ContextualPrefix != "ctx" {
		t.Errorf("prefix not swapped: got %q", out[0].ContextualPrefix)
	}
}

func TestSwapToParents_CollapsesDuplicateParents(t *testing.T) {
	t.Parallel()
	// Three children of the same parent + one flat row + one child of a
	// different parent. After swap+dedup we should keep: first child of
	// parentA (swapped), the flat row, the child of parentB (swapped).
	docs := []RankedDoc{
		{ID: "c1", Content: "child 1", ParentChunkID: ptr("parentA"), Score: 0.9},
		{ID: "c2", Content: "child 2", ParentChunkID: ptr("parentA"), Score: 0.8},
		{ID: "flat", Content: "flat row"},
		{ID: "c3", Content: "child 3", ParentChunkID: ptr("parentA"), Score: 0.7},
		{ID: "c4", Content: "child 4", ParentChunkID: ptr("parentB"), Score: 0.6},
	}
	parents := map[string]ParentChunkRow{
		"parentA": {ID: "parentA", Content: "PARENT A"},
		"parentB": {ID: "parentB", Content: "PARENT B"},
	}
	out, swapped, dropped := swapToParents(docs, parents)
	if len(out) != 3 {
		t.Fatalf("dedup: want 3 out, got %d", len(out))
	}
	if swapped != 2 {
		t.Errorf("dedup: want 2 swapped (one per unique parent), got %d", swapped)
	}
	if dropped != 2 {
		t.Errorf("dedup: want 2 dropped (the c2, c3 dupes), got %d", dropped)
	}
	if out[0].ID != "c1" || out[0].Content != "PARENT A" {
		t.Errorf("first kept: want id=c1 content=PARENT A, got id=%q content=%q", out[0].ID, out[0].Content)
	}
	if out[1].ID != "flat" || out[1].Content != "flat row" {
		t.Errorf("flat row not preserved in order: got id=%q content=%q", out[1].ID, out[1].Content)
	}
	if out[2].ID != "c4" || out[2].Content != "PARENT B" {
		t.Errorf("third kept: want id=c4 content=PARENT B, got id=%q content=%q", out[2].ID, out[2].Content)
	}
}

func TestSwapToParents_KeepsFirstWhenParentMissing(t *testing.T) {
	t.Parallel()
	// Parent lookup failed for parentA — first c1 is kept (with original
	// child content) and still consumes the dedup slot, so c2 (also
	// parentA) is dropped. Keeps result-set populated under parent-table
	// flakiness rather than collapsing the entire group.
	docs := []RankedDoc{
		{ID: "c1", Content: "child 1 text", ParentChunkID: ptr("parentA")},
		{ID: "c2", Content: "child 2 text", ParentChunkID: ptr("parentA")},
	}
	out, swapped, dropped := swapToParents(docs, map[string]ParentChunkRow{}) // empty lookup
	if len(out) != 1 {
		t.Fatalf("missing parent: want 1 out (still deduped), got %d", len(out))
	}
	if swapped != 0 {
		t.Errorf("missing parent: want 0 swapped, got %d", swapped)
	}
	if dropped != 1 {
		t.Errorf("missing parent: want 1 dropped (c2), got %d", dropped)
	}
	if out[0].Content != "child 1 text" {
		t.Errorf("missing parent: kept row content should remain child 1 text, got %q", out[0].Content)
	}
}

func TestCollectParentIDs_DedupsAndPreservesOrder(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", ParentChunkID: ptr("P1")},
		{ID: "b"}, // flat row
		{ID: "c", ParentChunkID: ptr("P2")},
		{ID: "d", ParentChunkID: ptr("P1")}, // duplicate of P1
		{ID: "e", ParentChunkID: ptr("")},   // empty string — treat as nil
	}
	ids := collectParentIDs(docs)
	if len(ids) != 2 {
		t.Fatalf("want 2 unique ids, got %d (%v)", len(ids), ids)
	}
	if ids[0] != "P1" || ids[1] != "P2" {
		t.Errorf("order/values: got %v, want [P1 P2]", ids)
	}
}
