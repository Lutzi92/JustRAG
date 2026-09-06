package chat

// Tests for the chat_condense_keep_raw_enabled gate (Wave 1 Task 7). The
// pure RawQueryForRetrieval table test moved to condense_history_test.go
// (Wave 2 Task 2), alongside CondenseFromHistory which shares its origin
// in CondenseFollowUp.

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
