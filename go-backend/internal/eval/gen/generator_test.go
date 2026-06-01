package gen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/vector"
)

type fakeCorpus struct {
	files  []FileRef
	chunks map[string][]vector.FileChunkRow
}

func (f *fakeCorpus) ListFiles(ctx context.Context, kbID string) ([]FileRef, error) {
	return f.files, nil
}
func (f *fakeCorpus) GetChunks(ctx context.Context, kbID, fileID string) ([]vector.FileChunkRow, error) {
	return f.chunks[fileID], nil
}

type fakeQGen struct{}

func (fakeQGen) LookupQuestions(ctx context.Context, fileName, doc, chunk string, n int) ([]string, error) {
	return []string{"What is X?"}, nil
}
func (fakeQGen) ComplexQuestion(ctx context.Context, fileName, doc, chunk string) (string, error) {
	return "Why does X imply Y?", nil
}
func (fakeQGen) EnumerationQuestion(ctx context.Context, fileName string, chunks []string) (string, error) {
	return "List all the steps in X.", nil
}
func (fakeQGen) BridgeQuestion(ctx context.Context, aName, aText, bName, bText string) (string, error) {
	return "How does X in A relate to Y in B?", nil
}

type fakeNeighbors struct{}

func (fakeNeighbors) NearestOtherFile(ctx context.Context, kbID, seedFileID, seedText string) (Neighbor, bool, error) {
	return Neighbor{FileID: "f2", FileName: "B.pdf", ChunkText: "b text", Score: 0.8}, true, nil
}

func newTestGen() *Generator {
	corpus := &fakeCorpus{
		files: []FileRef{{ID: "f1", Name: "A.pdf"}, {ID: "f2", Name: "B.pdf"}},
		chunks: map[string][]vector.FileChunkRow{
			"f1": {{ID: "c1", Content: "alpha content long enough to use"}},
			"f2": {{ID: "c2", Content: "beta content long enough to use"}},
		},
	}
	return New(corpus, fakeQGen{}, fakeNeighbors{}, 0.5)
}

func TestLookupMode(t *testing.T) {
	g := newTestGen()
	qs, err := g.lookup(context.Background(), "kb", "en", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 {
		t.Fatal("expected lookup questions")
	}
	for _, q := range qs {
		if q.QueryType != "lookup" {
			t.Errorf("query_type=%q want lookup", q.QueryType)
		}
		if len(q.MustCiteFileIDs) != 1 || len(q.MustCiteFileNames) != 1 {
			t.Errorf("expected single must_cite id+name, got ids=%v names=%v", q.MustCiteFileIDs, q.MustCiteFileNames)
		}
		if q.Language != "en" {
			t.Errorf("language=%q want en", q.Language)
		}
	}
}

func TestDedup(t *testing.T) {
	in := []question{
		{Question: "What is X?"}, {Question: "what  is x?"}, {Question: "Different?"},
	}
	out := dedup(in)
	if len(out) != 2 {
		t.Errorf("dedup got %d want 2", len(out))
	}
}

func TestEnumerationMode(t *testing.T) {
	g := newTestGen()
	qs, err := g.enumeration(context.Background(), "kb", "en", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 {
		t.Fatal("expected enumeration questions")
	}
	for _, q := range qs {
		if q.QueryType != "enumeration" {
			t.Errorf("query_type=%q want enumeration", q.QueryType)
		}
		if len(q.MustCiteFileIDs) != 1 {
			t.Errorf("enumeration should cite one file, got %v", q.MustCiteFileIDs)
		}
	}
}

func TestMultiHopMode(t *testing.T) {
	g := newTestGen()
	qs, err := g.multiHop(context.Background(), "kb", "en", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 {
		t.Fatal("expected multi-hop questions")
	}
	for _, q := range qs {
		if len(q.MustCiteFileIDs) != 2 {
			t.Fatalf("multi-hop must cite 2 files, got %v", q.MustCiteFileIDs)
		}
		if q.MustCiteFileIDs[0] == q.MustCiteFileIDs[1] {
			t.Errorf("multi-hop must cite two DISTINCT files, got %v", q.MustCiteFileIDs)
		}
		if q.QueryType != "complex_reasoning" {
			t.Errorf("multi-hop query_type=%q want complex_reasoning", q.QueryType)
		}
	}
}

func TestGenerateAllRoundTrips(t *testing.T) {
	g := newTestGen()
	qs, stats, err := g.GenerateAll(context.Background(), "kb", "en", Counts{Lookup: 2, Complex: 1, Enumeration: 1, MultiHop: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 || stats.Total() == 0 {
		t.Fatal("expected questions + stats")
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for _, q := range qs {
		if err := enc.Encode(&q); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := eval.ParseGoldenSetJSONL(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatalf("generated set failed validation: %v", err)
	}
	if len(parsed) != len(qs) {
		t.Errorf("round-trip count mismatch: %d vs %d", len(parsed), len(qs))
	}
}
