package app

import (
	"context"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/pipeline"
)

// workflowBindings adapts *agentteams.Store to the workflow handler's
// bindingSource interface.
//
// It exists because internal/pipeline is a leaf package and must not import
// internal/agentteams, while agentteams' read returns its own DTOs. The
// translation is mechanical and belongs to whichever package already imports
// both — this one, which is the wiring. It lives here for the same reason
// communitySink does, and does the same amount of thinking: none. Which option
// is the KB default is decided in internal/pipeline (bindingInfoFrom), where it
// is tested.
//
// It reads ListBindingCandidatesForKB, NOT ListAttachedForKB. That is a
// deliberate split, and the one place it could be undone by accident: the
// picker's read filters agents.is_enabled away, which is right for chat (a
// disabled agent is not selectable, and chat-time resolution rejects it anyway)
// and wrong for the canvas, where a default pointing at a disabled agent has to
// be visible so an admin can clear it. Pointing this back at ListAttachedForKB
// would silently restore "the graph denies the existence of a row the admin
// needs to clear"; pointing the PICKER at this one would make disabled agents
// selectable in chat.
type workflowBindings struct{ store *agentteams.Store }

// ListBindingOptions implements the workflow handler's bindingSource.
//
// Permission note: no access check here. The route above it is
// kbAdvancedChain (RequireRole api-user/admin/superadmin + RequireKBRole
// admin), which strictly outranks the kbViewChain that already
// serves the attached list via GET /api/kb/{id}/agents, and ListKBAgents adds
// no handler-internal check of its own. This read returns the same rows plus
// the disabled ones, all of them facts about the caller's own KB — no new
// permission surface.
func (b workflowBindings) ListBindingOptions(ctx context.Context, kbID string) ([]pipeline.BindingOption, error) {
	cands, err := b.store.ListBindingCandidatesForKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	return bindingOptionsFrom(cands), nil
}

// bindingOptionsFrom is the translation itself, split out from the store call
// so it can be tested without a database. Three fields are invisible when they
// go wrong: IsDefault (drop it and every KB projects "nothing bound"), Disabled
// and EmptyTeam (drop either and a default that cannot run is projected as if
// it ran, which is the lie the whole node exists to avoid).
func bindingOptionsFrom(cands []agentteams.BindingCandidate) []pipeline.BindingOption {
	out := make([]pipeline.BindingOption, 0, len(cands))
	for _, c := range cands {
		var kind pipeline.BindingKind
		switch c.Kind {
		case "agent":
			kind = pipeline.BindingAgent
		case "team":
			kind = pipeline.BindingTeam
		default:
			// Unreachable: the two store queries select the literal themselves.
			// Dropped rather than guessed at, because Kind is what decides
			// which endpoint the inspector PUTs to — an option mislabelled
			// "team" would write to …/teams/{agentId} and 404, while a missing
			// option is merely absent from a dropdown.
			continue
		}
		out = append(out, pipeline.BindingOption{
			Kind: kind, ID: c.ID, Name: c.Name,
			IsDefault: c.IsDefault, Disabled: !c.IsEnabled,
			// Teams only, and gated on kind rather than on the count alone:
			// the agent query reports 1 as a not-applicable placeholder (see
			// BindingCandidate.EnabledMemberCount), and an agent that somehow
			// arrived with 0 must not be told about members it cannot have.
			EmptyTeam: kind == pipeline.BindingTeam && c.EnabledMemberCount == 0,
		})
	}
	return out
}
