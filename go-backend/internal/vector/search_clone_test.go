package vector

import (
	"context"
	"testing"
)

type stubReader struct{ v string }

func (s stubReader) GetSiteConfigValue(_ context.Context, _ string) (*string, error) {
	val := s.v
	return &val, nil
}

func TestCloneWithSiteConfigReader(t *testing.T) {
	orig := &SearchService{siteConfig: stubReader{v: "orig"}}
	clone := orig.CloneWithSiteConfigReader(stubReader{v: "clone"})

	if clone == orig {
		t.Fatal("clone must be a distinct instance")
	}
	got, _ := clone.siteConfig.GetSiteConfigValue(context.Background(), "k")
	if got == nil || *got != "clone" {
		t.Fatalf("clone reader not installed: %v", got)
	}
	// Original must be untouched (no shared mutation).
	og, _ := orig.siteConfig.GetSiteConfigValue(context.Background(), "k")
	if og == nil || *og != "orig" {
		t.Fatalf("original reader mutated: %v", og)
	}
}
