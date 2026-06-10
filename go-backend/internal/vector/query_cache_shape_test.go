package vector

import (
	"bytes"
	"testing"
)

func TestShapeHash_DeterministicForSameOptions(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeLookup, FileIDs: []string{"a", "b"}, HyDE: true, MultiQuery: false, StepBack: false}
	b := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeLookup, FileIDs: []string{"a", "b"}, HyDE: true, MultiQuery: false, StepBack: false}
	if !bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("identical options should hash identically")
	}
}

func TestShapeHash_FileIDOrderingIgnored(t *testing.T) {
	t.Parallel()
	a := SearchOptions{FileIDs: []string{"a", "b", "c"}}
	b := SearchOptions{FileIDs: []string{"c", "b", "a"}}
	if !bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("FileIDs should be sorted before hashing")
	}
}

func TestShapeHash_DifferentEnhanceProducesDifferentHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Enhance: "rewrite"}
	b := SearchOptions{Enhance: "expand"}
	if bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("differing Enhance must produce different hash")
	}
}

func TestShapeHash_DifferentQueryTypeProducesDifferentHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{QueryType: QueryTypeLookup}
	b := SearchOptions{QueryType: QueryTypeComplexReasoning}
	if bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("differing QueryType must produce different hash")
	}
}

func TestShapeHash_DifferentTopNProducesDifferentHash(t *testing.T) {
	t.Parallel()
	opts := SearchOptions{}
	if bytes.Equal(shapeHash(opts, 15, ""), shapeHash(opts, 30, "")) {
		t.Errorf("differing topN must produce different hash")
	}
}

func TestShapeHash_DifferentFlagsProduceDifferentHash(t *testing.T) {
	t.Parallel()
	for _, mut := range []func(*SearchOptions){
		func(o *SearchOptions) { o.HyDE = true },
		func(o *SearchOptions) { o.MultiQuery = true },
		func(o *SearchOptions) { o.StepBack = true },
	} {
		base := SearchOptions{}
		mutated := SearchOptions{}
		mut(&mutated)
		if bytes.Equal(shapeHash(base, 15, ""), shapeHash(mutated, 15, "")) {
			t.Errorf("flag mutation must produce different hash; %+v vs %+v", base, mutated)
		}
	}
}

func TestShapeHash_DifferentGradeProducesDifferentHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Grade: false}
	b := SearchOptions{Grade: true}
	if bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("differing Grade must produce different hash")
	}
}

func TestShapeHash_DifferentGraderModelProducesDifferentHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{GraderModel: "gpt-4"}
	b := SearchOptions{GraderModel: "claude-3-haiku"}
	if bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("differing GraderModel must produce different hash")
	}
}

func TestShapeHash_DifferentModelOverrideProducesDifferentHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{ModelOverride: "gpt-4"}
	b := SearchOptions{ModelOverride: "claude-3-haiku"}
	if bytes.Equal(shapeHash(a, 15, ""), shapeHash(b, 15, "")) {
		t.Errorf("differing ModelOverride must produce different hash")
	}
}

func TestShapeHash_DifferentEmbeddingModelProducesDifferentHash(t *testing.T) {
	t.Parallel()
	opts := SearchOptions{QueryType: QueryTypeLookup}
	a := shapeHash(opts, 15, "Octen/Octen-Embedding-8B")
	b := shapeHash(opts, 15, "Qwen/Qwen3-Embedding-4B")
	if bytes.Equal(a, b) {
		t.Errorf("differing embedding model must produce different hash")
	}
}

func TestShapeHash_SameEmbeddingModelProducesSameHash(t *testing.T) {
	t.Parallel()
	opts := SearchOptions{QueryType: QueryTypeLookup}
	a := shapeHash(opts, 15, "Octen/Octen-Embedding-8B")
	b := shapeHash(opts, 15, "Octen/Octen-Embedding-8B")
	if !bytes.Equal(a, b) {
		t.Errorf("same embedding model must produce same hash")
	}
}

func TestShapeHash_EmptyEmbeddingModelDiffersFromSet(t *testing.T) {
	t.Parallel()
	opts := SearchOptions{QueryType: QueryTypeLookup}
	a := shapeHash(opts, 15, "")
	b := shapeHash(opts, 15, "Octen/Octen-Embedding-8B")
	if bytes.Equal(a, b) {
		t.Errorf("unset embedding model must not collide with a set one")
	}
}
