package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCatalogChecker struct {
	has bool
	err error
}

func (f fakeCatalogChecker) HasDataForKB(_ context.Context, _ string) (bool, error) {
	return f.has, f.err
}

func TestMaybeChartGuidance(t *testing.T) {
	ctx := context.Background()
	on := &fakeSiteConfigReader{values: map[string]*string{"chat_tabular_charts_enabled": strPtr("true")}}
	off := &fakeSiteConfigReader{values: map[string]*string{}}

	// Flag off -> empty regardless of catalog.
	if g := maybeChartGuidance(ctx, off, fakeCatalogChecker{has: true}, "kb", "en"); g != "" {
		t.Fatal("flag off must yield no guidance")
	}
	// Flag on, nil catalog -> empty.
	if g := maybeChartGuidance(ctx, on, nil, "kb", "en"); g != "" {
		t.Fatal("nil catalog must yield no guidance")
	}
	// Flag on, catalog says no data -> empty.
	if g := maybeChartGuidance(ctx, on, fakeCatalogChecker{has: false}, "kb", "en"); g != "" {
		t.Fatal("no tabular data must yield no guidance")
	}
	// Flag on, catalog error -> empty (fail closed).
	if g := maybeChartGuidance(ctx, on, fakeCatalogChecker{err: errors.New("boom")}, "kb", "en"); g != "" {
		t.Fatal("catalog error must yield no guidance")
	}
	// Flag on + has data -> guidance present.
	g := maybeChartGuidance(ctx, on, fakeCatalogChecker{has: true}, "kb", "en")
	if !strings.Contains(g, "```chart") {
		t.Fatalf("expected chart guidance, got %q", g)
	}
}
