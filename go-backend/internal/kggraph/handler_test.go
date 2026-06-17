package kggraph_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/kggraph"
)

type fakeStore struct {
	graph kg.GraphOverview
	max   int
	err   error
}

func (f *fakeStore) GraphOverview(_ context.Context, _ string, maxNodes int) (kg.GraphOverview, error) {
	f.max = maxNodes
	return f.graph, f.err
}

func (f *fakeStore) KBHasActiveIngestion(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func TestGetGraph_Returns200(t *testing.T) {
	fs := &fakeStore{graph: kg.GraphOverview{
		Nodes: []kg.GraphNode{{ID: 1, Name: "Alice", Type: "Person", Degree: 2}},
		Edges: []kg.GraphEdge{{Source: 1, Target: 2, Rel: "knows"}},
	}}
	h := kggraph.NewHandler(fs)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/graph?maxNodes=50", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if fs.max != 50 {
		t.Errorf("expected maxNodes 50 passed to store, got %d", fs.max)
	}
	var resp kg.GraphOverview
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].Name != "Alice" {
		t.Errorf("unexpected nodes: %+v", resp.Nodes)
	}
	if len(resp.Edges) != 1 || resp.Edges[0].Rel != "knows" {
		t.Errorf("unexpected edges: %+v", resp.Edges)
	}
}

func TestGetGraph_InvalidMaxNodes_DefaultsToZero(t *testing.T) {
	fs := &fakeStore{}
	h := kggraph.NewHandler(fs)

	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/graph?maxNodes=abc", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Unparseable maxNodes ⇒ 0 forwarded; the store applies its own default.
	if fs.max != 0 {
		t.Errorf("expected maxNodes 0 on parse failure, got %d", fs.max)
	}
}
