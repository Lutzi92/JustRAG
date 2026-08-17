package app

import (
	"testing"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/pipeline"
)

// The adapter is a few lines of field copying, and two of those fields are
// invisible when they go wrong:
//
//   - IsDefault. Lose it and the workflow canvas reports "kein Standard-Agent
//     hinterlegt" on every KB in the deployment, with every test in
//     internal/pipeline still green — those run against a fake that sets the
//     flag itself.
//   - Disabled (the negation of the store's IsEnabled). Lose it and a KB whose
//     default points at a switched-off agent is projected as if that agent ran,
//     which is the one thing this node exists not to do.
//   - EmptyTeam (derived from EnabledMemberCount). Same failure one step along:
//     a team with nobody in it is dropped at chat time, and losing the flag
//     projects it as live.
//
// This is the only place the real translation is checked.
func TestBindingOptionsFromCarriesTheDefaultFlag(t *testing.T) {
	opts := bindingOptionsFrom([]agentteams.BindingCandidate{
		// EnabledMemberCount is 1 for agents by construction (see the field's
		// doc comment) and really counted for teams.
		{Kind: "agent", ID: "a1", Name: "Bibliotheks-Assistent", IsEnabled: true, EnabledMemberCount: 1},
		{Kind: "team", ID: "t1", Name: "Recherche-Team", IsDefault: true, IsEnabled: true, EnabledMemberCount: 2},
	})

	if len(opts) != 2 {
		t.Fatalf("got %d options, want the agent and the team: %+v", len(opts), opts)
	}

	byID := map[string]pipeline.BindingOption{}
	for _, o := range opts {
		byID[o.ID] = o
	}

	agent := byID["a1"]
	if agent.Kind != pipeline.BindingAgent {
		t.Errorf("agent Kind = %q, want %q", agent.Kind, pipeline.BindingAgent)
	}
	if agent.Name != "Bibliotheks-Assistent" {
		t.Errorf("agent Name = %q, want the attached agent's name", agent.Name)
	}
	if agent.IsDefault {
		t.Error("agent IsDefault = true — only the team was flagged in the fixture")
	}
	if agent.Disabled {
		t.Error("agent Disabled = true for an IsEnabled candidate — the flag is inverted")
	}

	team := byID["t1"]
	if team.Kind != pipeline.BindingTeam {
		t.Errorf("team Kind = %q, want %q", team.Kind, pipeline.BindingTeam)
	}
	if !team.IsDefault {
		t.Error("team IsDefault = false — the KB default was dropped in translation, " +
			"which projects every bound KB as „nichts hinterlegt\"")
	}
	if team.EmptyTeam {
		t.Error("team EmptyTeam = true for a team with 2 enabled members — a staffed " +
			"team would then be drawn as unable to answer")
	}
	if agent.EmptyTeam {
		t.Error("agent EmptyTeam = true — an agent has no membership, so this arm " +
			"must never fire for one")
	}
}

// The empty-team arm. A team can be attached, flagged default, and switched ON,
// and still take no turn at all: LoadTeamForChat filters its members by
// is_enabled, and resolveTeamSelection answers (nil, "empty_team") for zero
// members, so the FULL standard path runs. is_enabled alone cannot see that,
// which is why the store carries the count and this is the line that converts
// it into the projection's predicate.
func TestBindingOptionsFromFlagsATeamWithNoEnabledMembers(t *testing.T) {
	opts := bindingOptionsFrom([]agentteams.BindingCandidate{
		{Kind: "team", ID: "t1", Name: "Leeres Team", IsDefault: true, IsEnabled: true,
			EnabledMemberCount: 0},
	})

	if len(opts) != 1 {
		t.Fatalf("got %d options, want the empty team to survive translation: %+v",
			len(opts), opts)
	}
	if !opts[0].EmptyTeam {
		t.Error("EmptyTeam = false for a team with 0 enabled members — the canvas " +
			"would then project OrchTeam and demote the standard path for a binding " +
			"that can never fire")
	}
	if opts[0].Disabled {
		t.Error("Disabled = true — the team itself is switched ON; folding the two " +
			"together tells the admin to flip a switch that is already flipped")
	}
}

// The teams-only rule, pinned rather than commented: an agent has no
// membership, so nothing the count says may make an agent binding look unusable.
// A symmetric agent branch is exactly what the review warned against inventing.
func TestBindingOptionsFromNeverMarksAnAgentEmpty(t *testing.T) {
	opts := bindingOptionsFrom([]agentteams.BindingCandidate{
		{Kind: "agent", ID: "a1", Name: "Solo", IsDefault: true, IsEnabled: true,
			EnabledMemberCount: 0},
	})
	if len(opts) != 1 {
		t.Fatalf("got %d options: %+v", len(opts), opts)
	}
	if opts[0].EmptyTeam {
		t.Error("agent EmptyTeam = true — an agent runs alone by definition; " +
			"marking it empty would make every single-agent binding project inactive")
	}
}

// The disabled arm, in its own test because it is the whole point of the
// second store read: a KB whose default points at a switched-off agent must
// still produce an option, flagged Disabled, or the canvas cannot show the row
// the admin has to clear.
func TestBindingOptionsFromFlagsADisabledCandidate(t *testing.T) {
	opts := bindingOptionsFrom([]agentteams.BindingCandidate{
		{Kind: "agent", ID: "a1", Name: "Abgeschaltet", IsDefault: true, IsEnabled: false},
	})

	if len(opts) != 1 {
		t.Fatalf("got %d options, want the disabled agent to survive translation: %+v",
			len(opts), opts)
	}
	if !opts[0].Disabled {
		t.Error("Disabled = false for a candidate with IsEnabled=false — the canvas " +
			"would then project a switched-off agent as if it answered")
	}
	if !opts[0].IsDefault {
		t.Error("IsDefault = false — a disabled binding is still THE binding, and " +
			"losing the flag is what made it unclearable")
	}
}

// A KB with nothing attached is a real, common state, and it must not be
// mistaken for a failed read: no candidates resolve to "nothing bound", the
// error path resolves to pipeline.BindingUnknown, and only the handler may
// decide which.
func TestBindingOptionsFromEmptyKB(t *testing.T) {
	if got := bindingOptionsFrom(nil); len(got) != 0 {
		t.Errorf("got %+v, want no options", got)
	}
}
