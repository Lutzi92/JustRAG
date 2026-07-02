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
