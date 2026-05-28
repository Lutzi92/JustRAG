package eval

import "testing"

func TestApplyRunKBOverride(t *testing.T) {
	const runKB = "11111111-1111-1111-1111-111111111111"

	t.Run("rewrites every question's KbID", func(t *testing.T) {
		qs := []Question{
			{ID: "q1", KbID: "old-a", MustCiteFileIDs: []string{"f1"}},
			{ID: "q2", KbID: "old-b", MustCiteFileIDs: []string{"f2"}},
			{ID: "q3", KbID: "old-c", MustCiteFileIDs: []string{"f3"}},
		}
		applyRunKBOverride(qs, runKB)
		for _, q := range qs {
			if q.KbID != runKB {
				t.Errorf("question %s: KbID=%q, want %q", q.ID, q.KbID, runKB)
			}
		}
	})

	t.Run("preserves other question fields", func(t *testing.T) {
		qs := []Question{{
			ID:              "q1",
			Question:        "warum?",
			KbID:            "old-a",
			Language:        "de",
			MustCiteFileIDs: []string{"f1", "f2"},
			QueryType:       "lookup",
			Notes:           "n",
		}}
		applyRunKBOverride(qs, runKB)
		got := qs[0]
		want := Question{
			ID: "q1", Question: "warum?", KbID: runKB, Language: "de",
			MustCiteFileIDs: []string{"f1", "f2"}, QueryType: "lookup", Notes: "n",
		}
		if got.ID != want.ID || got.Question != want.Question ||
			got.KbID != want.KbID || got.Language != want.Language ||
			got.QueryType != want.QueryType || got.Notes != want.Notes ||
			len(got.MustCiteFileIDs) != len(want.MustCiteFileIDs) {
			t.Errorf("fields mutated unexpectedly:\ngot  %+v\nwant %+v", got, want)
		}
	})

	t.Run("nil and empty slices do not panic", func(t *testing.T) {
		applyRunKBOverride(nil, runKB)
		applyRunKBOverride([]Question{}, runKB)
	})
}
