package eval

import (
	"context"
	"testing"
)

func TestSnapshotConfigReader_KnownKey(t *testing.T) {
	snap := map[string]*string{
		"mmr_lambda":         ptrStr("0.7"),
		"rerank_blend_alpha": ptrStr("0.5"),
		"crag_enabled":       ptrStr("true"),
	}
	r := NewSnapshotConfigReader(snap)
	v, err := r.GetSiteConfigValue(context.Background(), "mmr_lambda")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("expected non-nil value for known key")
	}
	if *v != "0.7" {
		t.Errorf("expected '0.7', got %q", *v)
	}
}

func TestSnapshotConfigReader_UnknownKey(t *testing.T) {
	r := NewSnapshotConfigReader(map[string]*string{"foo": ptrStr("bar")})
	v, err := r.GetSiteConfigValue(context.Background(), "not_in_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil for unknown key, got %v", v)
	}
}

func TestSnapshotConfigReader_NilValue(t *testing.T) {
	snap := map[string]*string{
		"key_with_nil_value": nil,
	}
	r := NewSnapshotConfigReader(snap)
	v, err := r.GetSiteConfigValue(context.Background(), "key_with_nil_value")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil value, got %v", v)
	}
}

func TestSnapshotConfigReader_EmptySnapshot(t *testing.T) {
	r := NewSnapshotConfigReader(map[string]*string{})
	v, err := r.GetSiteConfigValue(context.Background(), "any_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil for any key in empty snapshot, got %v", v)
	}
}

func TestSnapshotConfigReader_NilMapInput(t *testing.T) {
	r := NewSnapshotConfigReader(nil)
	v, err := r.GetSiteConfigValue(context.Background(), "any_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil for any key when initialized with nil, got %v", v)
	}
}

// Helper to create string pointers for test data
func ptrStr(s string) *string {
	return &s
}
