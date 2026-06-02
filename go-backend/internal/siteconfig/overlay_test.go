package siteconfig

import (
	"context"
	"testing"
)

// fakeBase is a per-key + batch reader used as the global base.
type fakeBase struct{ m map[string]*string }

func (b fakeBase) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	return b.m[k], nil
}
func (b fakeBase) GetSiteConfigValues(_ context.Context, keys []string) (map[string]*string, error) {
	out := map[string]*string{}
	for _, k := range keys {
		out[k] = b.m[k]
	}
	return out, nil
}

func sp(s string) *string { return &s }

func TestOverlay_RegistryKeyOverridden(t *testing.T) {
	base := fakeBase{m: map[string]*string{"rerank_blend_alpha": sp("0.8")}}
	ov := NewKBOverlay(base, map[string]*string{"rerank_blend_alpha": sp("0.3")})

	got, _ := ov.GetSiteConfigValue(context.Background(), "rerank_blend_alpha")
	if got == nil || *got != "0.3" {
		t.Fatalf("want override 0.3, got %v", got)
	}
}

func TestOverlay_RegistryKeyFallsThrough(t *testing.T) {
	base := fakeBase{m: map[string]*string{"rerank_blend_alpha": sp("0.8")}}
	ov := NewKBOverlay(base, map[string]*string{}) // no override for this key

	got, _ := ov.GetSiteConfigValue(context.Background(), "rerank_blend_alpha")
	if got == nil || *got != "0.8" {
		t.Fatalf("want global 0.8, got %v", got)
	}
}

func TestOverlay_NonRegistryKeyAlwaysGlobal(t *testing.T) {
	base := fakeBase{m: map[string]*string{"jwt_secret": sp("real")}}
	// Even if an override map smuggles a non-registry key, it must be ignored.
	ov := NewKBOverlay(base, map[string]*string{"jwt_secret": sp("attacker")})

	got, _ := ov.GetSiteConfigValue(context.Background(), "jwt_secret")
	if got == nil || *got != "real" {
		t.Fatalf("non-registry key must use global, got %v", got)
	}
}

func TestOverlay_BatchAppliesOverrides(t *testing.T) {
	base := fakeBase{m: map[string]*string{"rerank_blend_alpha": sp("0.8"), "jwt_secret": sp("real")}}
	ov := NewKBOverlay(base, map[string]*string{"rerank_blend_alpha": sp("0.3"), "jwt_secret": sp("attacker")})

	got, _ := ov.GetSiteConfigValues(context.Background(), []string{"rerank_blend_alpha", "jwt_secret"})
	if got["rerank_blend_alpha"] == nil || *got["rerank_blend_alpha"] != "0.3" {
		t.Fatalf("batch: want override 0.3, got %v", got["rerank_blend_alpha"])
	}
	if got["jwt_secret"] == nil || *got["jwt_secret"] != "real" {
		t.Fatalf("batch: non-registry must be global, got %v", got["jwt_secret"])
	}
}
