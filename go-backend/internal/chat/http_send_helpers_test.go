package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/tabular"
)

// fakeCatalogChecker is a TabularCatalogSummariser test double. Pointer
// receivers so listByKBCalls is a usable call counter across repeated
// invocations against the same instance (the cache tests rely on this).
type fakeCatalogChecker struct {
	has bool
	err error

	entries []tabular.CatalogEntry
	listErr error

	listByKBCalls int
}

func (f *fakeCatalogChecker) HasDataForKB(_ context.Context, _ string) (bool, error) {
	return f.has, f.err
}

func (f *fakeCatalogChecker) ListByKB(_ context.Context, _ string) ([]tabular.CatalogEntry, error) {
	f.listByKBCalls++
	return f.entries, f.listErr
}

// oneTableEntry is a minimal CatalogEntry that renders non-empty
// CompactSchema text (renderTableBlock always emits at least a heading
// line for a "table"-kind entry).
func oneTableEntry() tabular.CatalogEntry {
	return tabular.CatalogEntry{
		TableName: "sheet1",
		SheetName: "Sheet1",
		FileName:  "f.xlsx",
		SheetKind: "table",
		RowCount:  3,
	}
}

func TestMaybeTabularGuidance_BothFlagsOff(t *testing.T) {
	ctx := context.Background()
	off := &fakeSiteConfigReader{values: map[string]*string{}}
	cat := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	if g := maybeTabularGuidance(ctx, off, cat, "kb", "en"); g != "" {
		t.Fatalf("both flags off must yield no guidance, got %q", g)
	}
}

func TestMaybeTabularGuidance_NilCatalog(t *testing.T) {
	ctx := context.Background()
	on := &fakeSiteConfigReader{values: map[string]*string{"chat_tabular_charts_enabled": strPtr("true")}}
	if g := maybeTabularGuidance(ctx, on, nil, "kb", "en"); g != "" {
		t.Fatalf("nil catalog must yield no guidance, got %q", g)
	}
}

func TestMaybeTabularGuidance_HasDataForKBFalse(t *testing.T) {
	ctx := context.Background()
	on := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled":  strPtr("true"),
		"chat_tabular_charts_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: false}
	if g := maybeTabularGuidance(ctx, on, cat, "kb", "en"); g != "" {
		t.Fatalf("no tabular data must yield no guidance, got %q", g)
	}
}

func TestMaybeTabularGuidance_CatalogError(t *testing.T) {
	ctx := context.Background()
	on := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled":  strPtr("true"),
		"chat_tabular_charts_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{err: errors.New("boom")}
	if g := maybeTabularGuidance(ctx, on, cat, "kb", "en"); g != "" {
		t.Fatalf("catalog error must yield no guidance (fail closed), got %q", g)
	}
}

// TestMaybeTabularGuidance_MasterOnChartsOff: the full catalog summary +
// rules render, but no chart guidance.
func TestMaybeTabularGuidance_MasterOnChartsOff(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "en")
	if !strings.Contains(g, "## Tables") {
		t.Errorf("expected the Tables rules block, got %q", g)
	}
	if !strings.Contains(g, "sheet1") {
		t.Errorf("expected the catalog summary text, got %q", g)
	}
	if strings.Contains(g, "```chart") {
		t.Errorf("charts off must not include chart guidance, got %q", g)
	}
}

// German variant of the same case, checking the German rules heading.
func TestMaybeTabularGuidance_MasterOnChartsOff_German(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "de")
	if !strings.Contains(g, "## Tabellen") {
		t.Errorf("expected the German Tabellen rules block, got %q", g)
	}
	if strings.Contains(g, "```chart") {
		t.Errorf("charts off must not include chart guidance, got %q", g)
	}
}

// TestMaybeTabularGuidance_MasterOnChartsOn: both the catalog summary/rules
// AND chart guidance render.
func TestMaybeTabularGuidance_MasterOnChartsOn(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled":  strPtr("true"),
		"chat_tabular_charts_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "en")
	if !strings.Contains(g, "## Tables") {
		t.Errorf("expected the Tables rules block, got %q", g)
	}
	if !strings.Contains(g, "sheet1") {
		t.Errorf("expected the catalog summary text, got %q", g)
	}
	if !strings.Contains(g, "```chart") {
		t.Errorf("expected chart guidance, got %q", g)
	}
}

// TestMaybeTabularGuidance_MasterOffChartsOn: chart-only guidance (the
// pre-Task-9 behaviour), no TABLES fence.
func TestMaybeTabularGuidance_MasterOffChartsOn(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_charts_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "en")
	if !strings.Contains(g, "```chart") {
		t.Fatalf("expected chart guidance, got %q", g)
	}
	if strings.Contains(g, "TABLES") {
		t.Errorf("master off must not include the TABLES fence, got %q", g)
	}
	// The master flag being off must short-circuit before ever reading the
	// catalog listing.
	if cat.listByKBCalls != 0 {
		t.Errorf("master off must not call ListByKB, got %d calls", cat.listByKBCalls)
	}
}

// TestMaybeTabularGuidance_EmptyCatalogPlaceholder covers R41: HasDataForKB
// says the KB has tables, but ListByKB / CompactSchema renders empty text
// (e.g. every catalog row was filtered) — the placeholder line must still
// appear so the rules block renders instead of silently degrading to
// chart-only-or-nothing.
func TestMaybeTabularGuidance_EmptyCatalogPlaceholder(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: nil}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "en")
	if !strings.Contains(g, "## Tables") {
		t.Errorf("expected the Tables rules block, got %q", g)
	}
	if !strings.Contains(g, "(no tables catalogued)") {
		t.Errorf("expected the placeholder line, got %q", g)
	}
}

func TestMaybeTabularGuidance_EmptyCatalogPlaceholder_German(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}}
	cat := &fakeCatalogChecker{has: true, entries: nil}

	g := maybeTabularGuidance(ctx, reader, cat, "kb", "de")
	if !strings.Contains(g, "## Tabellen") {
		t.Errorf("expected the Tabellen rules block, got %q", g)
	}
	if !strings.Contains(g, "(keine Tabellen katalogisiert)") {
		t.Errorf("expected the German placeholder line, got %q", g)
	}
}

// TestCachedTabularCatalog_TTL exercises the ListByKB memoization directly:
// two calls within the TTL window must hit the underlying catalog once;
// past the TTL, a third call must hit it again.
func TestCachedTabularCatalog_TTL(t *testing.T) {
	ctx := context.Background()
	underlying := &fakeCatalogChecker{entries: []tabular.CatalogEntry{oneTableEntry()}}

	now := time.Now()
	clock := func() time.Time { return now }
	cached := newCachedTabularCatalog(underlying, func() time.Time { return clock() })

	if _, err := cached.ListByKB(ctx, "kb"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	now = now.Add(30 * time.Second)
	if _, err := cached.ListByKB(ctx, "kb"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if underlying.listByKBCalls != 1 {
		t.Fatalf("expected 1 underlying ListByKB call within the TTL window, got %d", underlying.listByKBCalls)
	}

	now = now.Add(61 * time.Second)
	if _, err := cached.ListByKB(ctx, "kb"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if underlying.listByKBCalls != 2 {
		t.Fatalf("expected 2 underlying ListByKB calls after the TTL expired, got %d", underlying.listByKBCalls)
	}
}

// TestMaybeTabularGuidance_CacheAcrossCalls exercises the cache through
// maybeTabularGuidance itself, passing a cachedTabularCatalog as cat (the
// way WithTabularCatalog wires it in production): two calls within 60s call
// the underlying catalog's ListByKB once; after 61s, twice.
func TestMaybeTabularGuidance_CacheAcrossCalls(t *testing.T) {
	ctx := context.Background()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}}
	underlying := &fakeCatalogChecker{has: true, entries: []tabular.CatalogEntry{oneTableEntry()}}

	now := time.Now()
	cached := newCachedTabularCatalog(underlying, func() time.Time { return now })

	maybeTabularGuidance(ctx, reader, cached, "kb", "en")
	maybeTabularGuidance(ctx, reader, cached, "kb", "en")
	if underlying.listByKBCalls != 1 {
		t.Fatalf("expected 1 ListByKB call within the TTL window, got %d", underlying.listByKBCalls)
	}

	now = now.Add(61 * time.Second)
	maybeTabularGuidance(ctx, reader, cached, "kb", "en")
	if underlying.listByKBCalls != 2 {
		t.Fatalf("expected 2 ListByKB calls after the TTL expired, got %d", underlying.listByKBCalls)
	}
}
