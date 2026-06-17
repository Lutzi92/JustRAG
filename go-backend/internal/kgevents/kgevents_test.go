package kgevents

import "testing"

func TestChannel(t *testing.T) {
	if got := Channel("abc-123"); got != "kg:abc-123" {
		t.Fatalf("Channel() = %q, want %q", got, "kg:abc-123")
	}
}
