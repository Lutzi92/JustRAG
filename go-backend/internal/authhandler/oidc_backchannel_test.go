package authhandler

import (
	"encoding/json"
	"strings"
	"testing"
)

// logoutClaims builds a valid Back-Channel Logout 1.0 claim set, then applies
// the test's mutation so each case tweaks exactly one field.
func logoutClaims(mut func(*logoutTokenClaims)) logoutTokenClaims {
	c := logoutTokenClaims{
		Sub: "sub-123",
		Events: map[string]json.RawMessage{
			backchannelLogoutEvent: json.RawMessage(`{}`),
		},
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

func TestValidateLogoutClaims_Valid(t *testing.T) {
	if err := validateLogoutClaims(logoutClaims(nil)); err != nil {
		t.Fatalf("expected valid logout token, got %v", err)
	}
}

// A sid-only token (no sub) is still a structurally valid logout token.
func TestValidateLogoutClaims_SidOnly(t *testing.T) {
	c := logoutClaims(func(c *logoutTokenClaims) {
		c.Sub = ""
		c.Sid = "session-abc"
	})
	if err := validateLogoutClaims(c); err != nil {
		t.Fatalf("expected sid-only token valid, got %v", err)
	}
}

// Presence of a nonce means the token is an ID token, not a logout token (§2.4).
func TestValidateLogoutClaims_RejectsNonce(t *testing.T) {
	c := logoutClaims(func(c *logoutTokenClaims) { c.Nonce = "n-0S6_WzA2Mj" })
	err := validateLogoutClaims(c)
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce rejection, got %v", err)
	}
}

func TestValidateLogoutClaims_RejectsMissingSubAndSid(t *testing.T) {
	c := logoutClaims(func(c *logoutTokenClaims) { c.Sub = "" })
	err := validateLogoutClaims(c)
	if err == nil || !strings.Contains(err.Error(), "sub") {
		t.Fatalf("expected sub/sid rejection, got %v", err)
	}
}

func TestValidateLogoutClaims_RejectsMissingEvent(t *testing.T) {
	c := logoutClaims(func(c *logoutTokenClaims) { c.Events = map[string]json.RawMessage{} })
	err := validateLogoutClaims(c)
	if err == nil || !strings.Contains(err.Error(), "backchannel-logout") {
		t.Fatalf("expected missing-event rejection, got %v", err)
	}
}

// The backchannel-logout event member's value MUST be a JSON object.
func TestValidateLogoutClaims_RejectsNonObjectEvent(t *testing.T) {
	c := logoutClaims(func(c *logoutTokenClaims) {
		c.Events = map[string]json.RawMessage{backchannelLogoutEvent: json.RawMessage(`"oops"`)}
	})
	err := validateLogoutClaims(c)
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("expected non-object-event rejection, got %v", err)
	}
}
