package vector

import (
	"math"
	"sort"
	"testing"
)

func TestApplyFeedbackBoost(t *testing.T) {
	docs := []RankedDoc{
		{ID: "a", Score: 0.50},
		{ID: "b", Score: 0.49},
		{ID: "c", Score: 0.40},
	}
	net := map[string]int{"a": -3, "c": 4} // a downvoted, c upvoted
	ApplyFeedbackBoost(docs, net, 0.1)

	if docs[0].Score >= 0.50 {
		t.Fatalf("downvoted chunk a should drop, got %v", docs[0].Score)
	}
	if docs[2].Score <= 0.40 {
		t.Fatalf("upvoted chunk c should rise, got %v", docs[2].Score)
	}
	if math.Abs(docs[2].Score-0.40) > 0.1+1e-9 {
		t.Fatalf("boost exceeded weight bound: %v", docs[2].Score-0.40)
	}
	if docs[1].Score != 0.49 {
		t.Fatalf("unrated chunk b changed: %v", docs[1].Score)
	}

	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Score > docs[j].Score })
	if docs[len(docs)-1].ID != "a" {
		t.Fatalf("expected a last after downvote, order=%v", []string{docs[0].ID, docs[1].ID, docs[2].ID})
	}
}

func TestApplyFeedbackBoost_NoopWhenDisabled(t *testing.T) {
	docs := []RankedDoc{{ID: "a", Score: 0.5}}
	ApplyFeedbackBoost(docs, map[string]int{"a": 5}, 0) // weight 0 = off
	if docs[0].Score != 0.5 {
		t.Fatalf("weight 0 must be a no-op, got %v", docs[0].Score)
	}
}
