package chat

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

type fakeOverrideLister struct{ m map[string]*string }

func (f fakeOverrideLister) ListKBOverrides(_ context.Context, _ string) (map[string]*string, error) {
	return f.m, nil
}

type fakeGlobalReader struct{}

func (fakeGlobalReader) GetSiteConfigValue(_ context.Context, _ string) (*string, error) {
	return nil, nil
}

func TestForKB_NoStore_ReturnsSameHandler(t *testing.T) {
	h := &Handler{siteConfigReader: fakeGlobalReader{}}
	if got := h.forKB(context.Background(), "kb1"); got != h {
		t.Fatal("with no kbConfigStore, forKB must return the same handler")
	}
}

func TestForKB_NoOverrides_ReturnsSameHandler(t *testing.T) {
	h := &Handler{
		siteConfigReader: fakeGlobalReader{},
		kbConfigStore:    fakeOverrideLister{m: map[string]*string{}},
	}
	if got := h.forKB(context.Background(), "kb1"); got != h {
		t.Fatal("with no overrides, forKB must return the same handler (zero cost)")
	}
}

func TestForKB_WithOverrides_ReturnsClone(t *testing.T) {
	sp := func(s string) *string { return &s }
	shared := vector.NewSearchService(nil, nil, nil)
	h := &Handler{
		siteConfigReader: fakeGlobalReader{},
		searchService:    shared,
		kbConfigStore:    fakeOverrideLister{m: map[string]*string{"rerank_blend_alpha": sp("0.3")}},
	}
	got := h.forKB(context.Background(), "kb1")
	if got == h {
		t.Fatal("with overrides, forKB must return a distinct handler")
	}
	if got.searchService == h.searchService {
		t.Fatal("clone must carry its own SearchService bound to the overlay")
	}
	// The cloned reader resolves the override.
	v, _ := got.siteConfigReader.GetSiteConfigValue(context.Background(), "rerank_blend_alpha")
	if v == nil || *v != "0.3" {
		t.Fatalf("clone reader should resolve override, got %v", v)
	}
}
