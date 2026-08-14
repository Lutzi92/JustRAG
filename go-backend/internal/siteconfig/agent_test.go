package siteconfig

import (
	"context"
	"testing"
)

type agentFakeBase map[string]*string

func (f agentFakeBase) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	return f[key], nil
}

func strp(s string) *string { return &s }

func TestIsPerAgent(t *testing.T) {
	if !IsPerAgent("crag_enabled") {
		t.Fatal("crag_enabled should be per-agent overridable")
	}
	if IsPerAgent("raptor_enabled") {
		t.Fatal("ingestion-time (RequiresReingest) keys must not be per-agent")
	}
	if IsPerAgent("jwt_secret") {
		t.Fatal("non-registry keys must not be per-agent")
	}
}

func TestAgentFieldsExcludeReingest(t *testing.T) {
	for _, f := range AgentFields() {
		if f.RequiresReingest {
			t.Fatalf("AgentFields leaked reingest key %s", f.Key)
		}
	}
	if len(AgentFields()) == 0 {
		t.Fatal("AgentFields must not be empty")
	}
}

func TestValidateAgentConfig(t *testing.T) {
	if err := ValidateAgentConfig(map[string]string{"crag_enabled": "true"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := ValidateAgentConfig(map[string]string{"raptor_enabled": "true"}); err == nil {
		t.Fatal("reingest key must be rejected")
	}
	if err := ValidateAgentConfig(map[string]string{"crag_enabled": "banana"}); err == nil {
		t.Fatal("type-invalid value must be rejected")
	}
}

// TestVerificationKeysAreAgentOverridable pins spec §6.2: expanding the
// per-KB registry (Tasks 2-3) deliberately widens the per-agent surface too,
// because IsPerAgent is defined as the per-KB registry minus RequiresReingest
// keys. A user-created agent may therefore switch its own verification
// stages (factcheck, Self-RAG, ...) on or off, and those keys appear in the
// agent form. These are tuning keys, not operational or security keys --
// chat.RestrictedDispatcher still gates privileged tools (code_exec,
// sql_query, web_search) independently of anything a config overlay
// resolves.
//
// If this test ever needs to fail, the fix is a third predicate (an explicit
// agentExcluded set), NOT unregistering the key per-KB -- that would also
// remove it from the (legitimate) per-KB settings surface, not just the
// per-agent one.
//
// Each assertion is derived from the key's own registry entry
// (fld.RequiresReingest), not a hardcoded true/false per key name, so the
// test proves IsPerAgent's stated rule ("per-KB minus RequiresReingest") for
// both a positive and a negative case, rather than merely restating a list
// of keys that happen to pass today.
func TestVerificationKeysAreAgentOverridable(t *testing.T) {
	for _, key := range []string{"factcheck_in_chat", "chat_self_rag_enabled"} {
		fld, ok := Field(key)
		if !ok {
			t.Fatalf("%s must be a registered per-KB key for this test to mean anything", key)
		}
		if fld.RequiresReingest {
			t.Fatalf("%s is marked RequiresReingest; this test no longer exercises the intended (non-reingest) case", key)
		}
		if !IsPerAgent(key) {
			t.Fatalf("%s: verification-stage keys are deliberately per-agent overridable per spec §6.2 (tuning, not operational/security)", key)
		}
	}

	// Inverse: a RequiresReingest key stays excluded from the per-agent
	// surface even though it IS per-KB, proving IsPerAgent tracks
	// RequiresReingest rather than admitting every registered key.
	const reingestKey = "raptor_enabled"
	fld, ok := Field(reingestKey)
	if !ok || !fld.RequiresReingest {
		t.Fatalf("%s must be a registered RequiresReingest key for this test to mean anything", reingestKey)
	}
	if !IsPerKB(reingestKey) {
		t.Fatalf("%s must remain per-KB configurable; only the per-agent surface excludes it", reingestKey)
	}
	if IsPerAgent(reingestKey) {
		t.Fatalf("%s is ingestion-time (RequiresReingest); overriding it per chat turn would be a silent no-op, so it must stay excluded from the per-agent surface", reingestKey)
	}
}

func TestAgentOverlayResolution(t *testing.T) {
	base := agentFakeBase{"crag_enabled": strp("false"), "jwt_secret": strp("real")}
	ov := NewAgentOverlay(base, map[string]*string{
		"crag_enabled": strp("true"),
		"jwt_secret":   strp("attacker"), // non-agent key: must be ignored
	})
	ctx := context.Background()
	v, err := ov.GetSiteConfigValue(ctx, "crag_enabled")
	if err != nil || v == nil || *v != "true" {
		t.Fatalf("agent override not applied: %v %v", v, err)
	}
	v, _ = ov.GetSiteConfigValue(ctx, "jwt_secret")
	if v == nil || *v != "real" {
		t.Fatal("security key must resolve from base, never from agent overrides")
	}
}
