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
// keys. So these keys pass ValidateAgentConfig, persist on the agent record,
// and are rendered by AgentFields() in the agent form.
//
// WHAT THAT DOES *NOT* MEAN, as of this phase: a user-created agent CANNOT
// actually switch its own verification stages (factcheck, Self-RAG, the
// citation validator, the refine gate, ...) on or off. The agent overlay is
// built only inside params.SearcherForAgent and applied only via
// ss.CloneWithSiteConfigReader (internal/chat/http_send.go:660-671; same
// shape in internal/eval/team_adapter.go:122-132). h.siteConfigReader is
// never re-wrapped for an agent, so only keys read through
// internal/vector's SearchService resolve per-agent -- of the 18 keys this
// phase added, exactly three do: query_cache_enabled, recency_boost_enabled,
// chat_feedback_boost_enabled. The other 15, every verification stage among
// them, are read from the KB-overlaid reader in internal/chat and ignore
// agent config entirely.
//
// Consequence to be honest about: the agent form currently advertises ~15
// controls that are inert for agents. Closing that is a follow-up, not this
// wave, and there are exactly two honest closures -- wrap the agent's reader
// for the whole turn (h.siteConfigReader included), or narrow AgentFields()
// to the keys internal/vector actually reads. Until one lands, this test
// asserts only what is true: membership in the per-agent surface, i.e. that
// the key validates and persists, NOT that it takes effect.
//
// The security half of the original claim does hold and was independently
// confirmed: these are tuning keys, not operational or security keys --
// chat.RestrictedDispatcher gates privileged tools (code_exec, sql_query,
// web_search) independently of anything a config overlay resolves, and
// agents_allow_privileged_tools is not in the registry at all.
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
			t.Fatalf("%s: verification-stage keys are deliberately part of the per-agent surface per spec §6.2 "+
				"(tuning, not operational/security) — they validate and persist on an agent, even though "+
				"only the internal/vector-read keys currently take effect at answer time", key)
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
