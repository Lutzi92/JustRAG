package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageRowAttributionJSON(t *testing.T) {
	id := "t-1"
	b, err := json.Marshal(MessageRow{ID: "m1", Role: "ai", TeamID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"teamId":"t-1"`) {
		t.Fatalf("teamId json tag wrong: %s", b)
	}
	b, _ = json.Marshal(MessageRow{ID: "m2", Role: "user"})
	if strings.Contains(string(b), "teamId") {
		t.Fatalf("nil attribution must be omitted (byte-stability): %s", b)
	}
}
