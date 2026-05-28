package vector

import (
	"bytes"
	"testing"
)

func TestQueryCacheShape_SubQueriesAffectsHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, SubQueries: nil}
	b := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, SubQueries: []string{"alt1"}}
	c := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, SubQueries: []string{"alt1", "alt2"}}

	ha := shapeHash(a, 15)
	hb := shapeHash(b, 15)
	hc := shapeHash(c, 15)

	if bytes.Equal(ha, hb) || bytes.Equal(hb, hc) || bytes.Equal(ha, hc) {
		t.Errorf("expected distinct hashes for different SubQueries lengths; got\n  a: %x\n  b: %x\n  c: %x", ha, hb, hc)
	}
}

func TestQueryCacheShape_SubQueriesNilVsEmptySliceEqual(t *testing.T) {
	t.Parallel()
	// nil and empty slice both have length 0 — they must hash the same.
	a := SearchOptions{SubQueries: nil}
	b := SearchOptions{SubQueries: []string{}}
	if !bytes.Equal(shapeHash(a, 10), shapeHash(b, 10)) {
		t.Errorf("nil and empty SubQueries should hash identically (both len=0)")
	}
}

// TestQueryCacheShape_GraphChunkIDsAffectsHash: the AP-C4 graph
// router injects a list of chunk IDs into the search pool. Two
// turns differing only in the resolved subgraph size must NOT
// share a cache slot — the fused result depends on the injected
// list. Length-only hashing follows the same compromise as
// SubQueries: KG re-ingest changes the count → invalidates entry.
func TestQueryCacheShape_GraphChunkIDsAffectsHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, GraphChunkIDs: nil}
	b := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, GraphChunkIDs: []string{"c1"}}
	c := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, GraphChunkIDs: []string{"c1", "c2"}}

	ha := shapeHash(a, 15)
	hb := shapeHash(b, 15)
	hc := shapeHash(c, 15)

	if bytes.Equal(ha, hb) || bytes.Equal(hb, hc) || bytes.Equal(ha, hc) {
		t.Errorf("expected distinct hashes for different GraphChunkIDs lengths; got\n  a: %x\n  b: %x\n  c: %x", ha, hb, hc)
	}
}

func TestQueryCacheShape_GraphChunkIDsNilVsEmptySliceEqual(t *testing.T) {
	t.Parallel()
	a := SearchOptions{GraphChunkIDs: nil}
	b := SearchOptions{GraphChunkIDs: []string{}}
	if !bytes.Equal(shapeHash(a, 10), shapeHash(b, 10)) {
		t.Errorf("nil and empty GraphChunkIDs should hash identically (both len=0)")
	}
}

// TestQueryCacheShape_GraphChunkIDsAndSubQueriesIndependent: the
// two length-counted fields must each independently fragment the
// cache. A request with N=1 SubQueries and N=0 GraphChunkIDs must
// hash differently from one with N=0 SubQueries and N=1
// GraphChunkIDs — otherwise the two extra-list contributors would
// collide in the cache.
func TestQueryCacheShape_GraphChunkIDsAndSubQueriesIndependent(t *testing.T) {
	t.Parallel()
	a := SearchOptions{SubQueries: []string{"s1"}, GraphChunkIDs: nil}
	b := SearchOptions{SubQueries: nil, GraphChunkIDs: []string{"c1"}}
	if bytes.Equal(shapeHash(a, 10), shapeHash(b, 10)) {
		t.Error("SubQueries and GraphChunkIDs must hash to distinct positions")
	}
}
