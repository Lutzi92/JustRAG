package chat

// Tests for the rewrite ⊕ raw retrieval lane (Wave 1 Task 7): the
// chat_condense_keep_raw_enabled gate and the pure rawQueryForRetrieval
// helper that decides whether CondenseFollowUp's raw utterance is worth
// forwarding to vector.SearchOptions.RawQuery.

import (
	"context"
	"testing"
)

func TestChatCondenseKeepRawEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatCondenseKeepRawEnabled(context.Background(), reader) {
		t.Fatal("chat_condense_keep_raw_enabled must default to false")
	}
}

func TestChatCondenseKeepRawEnabled_ExplicitOn(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_condense_keep_raw_enabled": strPtr("true"),
	}}
	if !ChatCondenseKeepRawEnabled(context.Background(), reader) {
		t.Fatal("expected chat_condense_keep_raw_enabled to read true")
	}
}

func TestRawQueryForRetrieval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		enabled        bool
		raw, condensed string
		want           string
	}{
		{"disabled", false, "und der?", "Wer leitet den Workshop?", ""},
		{"identical", true, " Wer leitet den Workshop? ", "Wer leitet den Workshop?", ""},
		{"empty raw", true, "", "x", ""},
		{"condensed", true, "und wann?", "Wann fand der Workshop statt?", "und wann?"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := rawQueryForRetrieval(c.enabled, c.raw, c.condensed); got != c.want {
				t.Errorf("%s: want %q, got %q", c.name, c.want, got)
			}
		})
	}
}
