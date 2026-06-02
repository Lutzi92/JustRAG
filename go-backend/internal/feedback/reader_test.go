package feedback

import (
	"strings"
	"testing"
)

func TestNetSignalsSQL_MentionsRatingsAndTable(t *testing.T) {
	for _, want := range []string{"positive", "negative", "message_chunks", "ANY"} {
		if !strings.Contains(netSignalsSQL, want) {
			t.Fatalf("netSignalsSQL missing %q", want)
		}
	}
}

func TestTopNegativeSQL_OrdersAscending(t *testing.T) {
	if !strings.Contains(topNegativeSQL, "ORDER BY net ASC") {
		t.Fatalf("topNegativeSQL must order by net ascending")
	}
}
