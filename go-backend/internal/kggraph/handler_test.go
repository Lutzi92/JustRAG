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
	graph    kg.GraphOverview
	max      int
	err      error
	scoped   kg.ScopedGraph
	scopeErr error
}

func (f *fakeStore) GraphOverview(_ context.Context, _ string, maxNodes int) (kg.GraphOverview, error) {
	f.max = maxNodes
	return f.graph, f.err
}

func (f *fakeStore) KBHasActiveIngestion(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (f *fakeStore) ScopedGraphForMessage(_ context.Context, _, _ string) (kg.ScopedGraph, error) {
	return f.scoped, f.scopeErr
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

func TestGetGraph_ScopedByMessageID(t *testing.T) {
	store := &fakeStore{scoped: kg.ScopedGraph{
		Nodes: []kg.ScopedGraphNode{{ID: 1, Name: "X", Type: "concept", Sources: []kg.NodeSource{{FileID: "f", FileName: "report.pdf", ChunkID: "c"}}}},
		Edges: []kg.GraphEdge{},
	}}
	h := kggraph.NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/graph?messageId=11111111-1111-1111-1111-111111111111", nil)
	req.SetPathValue("id", "kb1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["scoped"] != true {
		t.Errorf("expected scoped=true, got %v", body["scoped"])
	}
	nodes, _ := body["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %v", body["nodes"])
	}
}

func TestGetGraph_ScopedMessageNotInKB_404(t *testing.T) {
	store := &fakeStore{scopeErr: kg.ErrMessageNotInKB}
	h := kggraph.NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/graph?messageId=22222222-2222-2222-2222-222222222222", nil)
	req.SetPathValue("id", "kb1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

func TestGetGraph_MalformedMessageID_400(t *testing.T) {
	store := &fakeStore{}
	h := kggraph.NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/graph?messageId=not-a-uuid", nil)
	req.SetPathValue("id", "kb1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

func TestGetGraph_NoMessageID_UsesOverview(t *testing.T) {
	store := &fakeStore{graph: kg.GraphOverview{Nodes: []kg.GraphNode{{ID: 9, Name: "Z"}}, Edges: []kg.GraphEdge{}}}
	h := kggraph.NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb1/graph", nil)
	req.SetPathValue("id", "kb1")
	rec := httptest.NewRecorder()
	h.GetGraph(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, hasScoped := body["scoped"]; hasScoped {
		t.Errorf("overview path must not set scoped flag")
	}
}
