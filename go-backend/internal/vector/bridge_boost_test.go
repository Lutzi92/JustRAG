package vector

import (
	"math"
	"testing"
)

func TestApplyBridgeBoost_FrequencyWeightedAndBounded(t *testing.T) {
	docs := []RankedDoc{
		{ID: "p1", Score: 1.0},
		{ID: "p2", Score: 1.0},
		{ID: "p3", Score: 1.0},
		{ID: "p5", Score: 1.0},
		{ID: "nb", Score: 1.0},
	}
	bridge := map[string]int{"p1": 1, "p2": 2, "p3": 3, "p5": 5}
	w := 0.3
	ApplyBridgeBoost(docs, bridge, w)
	want := map[string]float64{
		"p1": 1.0 + w*1.0/3.0,
		"p2": 1.0 + w*2.0/3.0,
		"p3": 1.0 + w,
		"p5": 1.0 + w,
		"nb": 1.0,
	}
	for i := range docs {
		if math.Abs(docs[i].Score-want[docs[i].ID]) > 1e-9 {
			t.Errorf("%s: got %v want %v", docs[i].ID, docs[i].Score, want[docs[i].ID])
		}
	}
}

func TestApplyBridgeBoost_NoOps(t *testing.T) {
	docs := []RankedDoc{{ID: "a", Score: 1.0}}
	ApplyBridgeBoost(docs, map[string]int{"a": 2}, 0)
	if docs[0].Score != 1.0 {
		t.Errorf("zero weight must be no-op, got %v", docs[0].Score)
	}
	ApplyBridgeBoost(docs, nil, 0.3)
	if docs[0].Score != 1.0 {
		t.Errorf("empty bridge must be no-op, got %v", docs[0].Score)
	}
}

func TestApplyBridgeBoost_BuriedBridgeOvertakesNearTie(t *testing.T) {
	docs := []RankedDoc{{ID: "nb", Score: 1.05}, {ID: "br", Score: 1.0}}
	ApplyBridgeBoost(docs, map[string]int{"br": 3}, 0.3)
	if docs[1].Score <= docs[0].Score {
		t.Errorf("boosted bridge (%v) should exceed non-bridge (%v)", docs[1].Score, docs[0].Score)
	}
}
