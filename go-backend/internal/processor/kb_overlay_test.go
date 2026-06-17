package processor

import (
	"context"
	"errors"
	"testing"
)

type fakeReader struct{ vals map[string]string }

func (f fakeReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

type fakeLister struct {
	overrides map[string]map[string]*string
	err       error
}

func (f fakeLister) ListKBOverrides(_ context.Context, kbID string) (map[string]*string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.overrides[kbID], nil
}

func ptr(s string) *string { return &s }

func read(p *Processor, key string) string {
	v, _ := p.siteConfigReader.GetSiteConfigValue(context.Background(), key)
	if v == nil {
		return ""
	}
	return *v
}

func TestWithKBConfig_OverlaysPerKBValue(t *testing.T) {
	p := &Processor{siteConfigReader: fakeReader{vals: map[string]string{
		"raptor_enabled":       "false",
		"parent_child_enabled": "global-only",
	}}}
	p.SetKBOverrideLister(fakeLister{overrides: map[string]map[string]*string{
		"kb1": {"raptor_enabled": ptr("true")},
	}})

	eff := p.withKBConfig(context.Background(), "kb1")
	if got := read(eff, "raptor_enabled"); got != "true" {
		t.Fatalf("per-KB override should win: got %q want %q", got, "true")
	}
	// Keys without a per-KB override must delegate to the global reader.
	if got := read(eff, "parent_child_enabled"); got != "global-only" {
		t.Fatalf("overlay must delegate to global for unset keys: got %q want %q", got, "global-only")
	}
}

func TestWithKBConfig_NoListerOrNoOverrides_ReturnsSame(t *testing.T) {
	base := fakeReader{vals: map[string]string{"raptor_enabled": "false"}}

	// No lister configured.
	p1 := &Processor{siteConfigReader: base}
	if p1.withKBConfig(context.Background(), "kb1") != p1 {
		t.Fatal("no lister: expected same *Processor")
	}

	// Empty kbID.
	p2 := &Processor{siteConfigReader: base}
	p2.SetKBOverrideLister(fakeLister{})
	if p2.withKBConfig(context.Background(), "") != p2 {
		t.Fatal("empty kbID: expected same *Processor")
	}

	// KB has no overrides.
	p3 := &Processor{siteConfigReader: base}
	p3.SetKBOverrideLister(fakeLister{overrides: map[string]map[string]*string{}})
	if p3.withKBConfig(context.Background(), "kb1") != p3 {
		t.Fatal("no overrides: expected same *Processor")
	}

	// Lookup error → fall back to same *Processor (fail-open).
	p4 := &Processor{siteConfigReader: base}
	p4.SetKBOverrideLister(fakeLister{err: errors.New("db down")})
	if p4.withKBConfig(context.Background(), "kb1") != p4 {
		t.Fatal("lookup error: expected same *Processor")
	}
}
