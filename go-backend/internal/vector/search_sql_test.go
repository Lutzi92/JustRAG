package vector

import "testing"

func TestExcludeCommunitySummaryClause(t *testing.T) {
	if got := excludeCommunitySummaryClause(""); got != " AND node_kind <> 'community_summary'" {
		t.Errorf("empty filter: got %q", got)
	}
	if got := excludeCommunitySummaryClause("community_summary"); got != "" {
		t.Errorf("filter set: want empty, got %q", got)
	}
	if got := excludeCommunitySummaryClause("leaf"); got != "" {
		t.Errorf("leaf filter: want empty, got %q", got)
	}
}
