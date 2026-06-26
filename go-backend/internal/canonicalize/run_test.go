package canonicalize

import (
	"context"
	"testing"
)

type fakeMerger struct{ calls [][3]any }

func (f *fakeMerger) MergeEntities(_ context.Context, kbID string, survivorID int64, mergedIDs []int64, aliasUnion []string) error {
	f.calls = append(f.calls, [3]any{survivorID, mergedIDs, aliasUnion})
	return nil
}

func TestRun_OnlyConfirmedPairsMerge(t *testing.T) {
	ents := []CanonEntity{
		{ID: 1, Name: "PPM", Type: "org", Degree: 1},
		{ID: 2, Name: "PPM-Team", Type: "org", Degree: 5},
		{ID: 3, Name: "Unrelated", Type: "org", Degree: 1},
	}
	embed := func(_ context.Context, texts []string) ([][]float64, error) {
		return [][]float64{{1, 0}, {0.99, 0.01}, {0, 1}}, nil
	}
	confirm := func(_ context.Context, _ []CanonEntity, pairs []Pair) ([]Pair, error) {
		var ok []Pair
		for _, p := range pairs {
			if p.I == 0 && p.J == 1 {
				ok = append(ok, p)
			}
		}
		return ok, nil
	}
	m := &fakeMerger{}
	res, err := Run(context.Background(), "kb", ents, embed, confirm, m, Config{Threshold: 0.9, MaxPairs: 100})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("expected exactly one merge, got %d", len(m.calls))
	}
	if got := m.calls[0][0].(int64); got != 2 {
		t.Errorf("survivor should be entity 2 (higher degree), got %d", got)
	}
	if res.Merged != 1 {
		t.Errorf("Result.Merged = %d want 1", res.Merged)
	}
}

func TestRun_EmbedErrorReturnsErrorNoMerge(t *testing.T) {
	ents := []CanonEntity{{ID: 1, Name: "A", Type: "org"}, {ID: 2, Name: "B", Type: "org"}}
	embed := func(context.Context, []string) ([][]float64, error) { return nil, context.DeadlineExceeded }
	m := &fakeMerger{}
	if _, err := Run(context.Background(), "kb", ents, embed, nil, m, Config{Threshold: 0.9, MaxPairs: 10}); err == nil {
		t.Error("embed error should propagate")
	}
	if len(m.calls) != 0 {
		t.Error("no merge on embed failure")
	}
}

func TestParseConfirm(t *testing.T) {
	pairs := []Pair{{I: 0, J: 1}, {I: 2, J: 3}}
	got, err := ParseConfirm(`{"verdicts":[{"index":0,"same":true},{"index":1,"same":false}]}`, pairs)
	if err != nil {
		t.Fatalf("ParseConfirm: %v", err)
	}
	if len(got) != 1 || got[0].I != 0 {
		t.Errorf("only pair 0 confirmed, got %+v", got)
	}
	// out-of-range index ignored.
	got2, _ := ParseConfirm(`{"verdicts":[{"index":9,"same":true}]}`, pairs)
	if len(got2) != 0 {
		t.Errorf("out-of-range index must be ignored, got %+v", got2)
	}
}

type mapR map[string]string

func (m mapR) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := m[k]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestConfigReaders(t *testing.T) {
	ctx := context.Background()
	if Enabled(ctx, mapR{}) {
		t.Error("default disabled")
	}
	if !Enabled(ctx, mapR{"kg_canonicalization_enabled": "true"}) {
		t.Error("true enables")
	}
	if got := Threshold(ctx, mapR{}); got != 0.85 {
		t.Errorf("default threshold 0.85, got %v", got)
	}
	if got := Threshold(ctx, mapR{"kg_canonicalization_threshold": "0.92"}); got != 0.92 {
		t.Errorf("threshold override, got %v", got)
	}
	if got := MaxPairs(ctx, mapR{}); got != 200 {
		t.Errorf("default maxpairs 200, got %v", got)
	}
}
