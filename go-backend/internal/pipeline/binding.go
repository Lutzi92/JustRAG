package pipeline

// BindingKind names what a KB's default agent/team binding points at.
//
// The binding itself lives in agent_kb_links.is_default and
// team_kb_links.is_default (migration 0061), one default per KB across BOTH
// tables — internal/agentteams' Store.attach clears every is_default on the KB
// before setting a new one, in the same transaction. BindingAgent and
// BindingTeam are therefore mutually exclusive by construction, which is why
// this is one enum rather than two independent fields.
type BindingKind string

const (
	// BindingNone is the zero value: the KB has no default bound. It is the
	// zero value on purpose, so a caller that has not resolved a binding yet
	// projects "nothing bound" rather than a wrong one.
	BindingNone BindingKind = ""
	// BindingAgent — a single user-created agent is the KB default.
	BindingAgent BindingKind = "agent"
	// BindingTeam — an agent team is the KB default.
	BindingTeam BindingKind = "team"
	// BindingUnknown — the caller TRIED to resolve the binding and could not
	// (the agent/team store errored). It is NOT the zero value: "nothing
	// bound" is a claim about the KB, and a failed read is not entitled to
	// make it. A KB whose agent listing is down would otherwise render
	// „kein Standard-Agent hinterlegt" on a KB that has one, and — worse —
	// project the orchestrator ladder as if no team gate existed.
	//
	// Bound() is false for it, so nothing downstream pretends a team runs
	// either. The uncertainty is stated on the node itself (applyAgentBinding)
	// rather than smuggled into either of the two confident answers.
	BindingUnknown BindingKind = "unknown"
)

// BindingOption is one agent or team attached to the KB, as a candidate for
// the KB default: resolved once by the workflow handler so the inspector needs
// no second request. Not every option is bindable — see Disabled.
//
// Kind is what tells the writer which endpoint to PUT to (…/agents/{id} vs
// …/teams/{id}), so it travels per option rather than by splitting the list in
// two — the inspector renders ONE dropdown over both.
type BindingOption struct {
	Kind BindingKind `json:"kind"`
	ID   string      `json:"id"`
	Name string      `json:"name"`

	// Disabled reports that the agent/team behind this option is switched off
	// (agents.is_enabled / agent_teams.is_enabled false). Such an option is
	// still LISTED, because the KB's current default may be one of them and an
	// admin has to see what they are about to clear — but it must not be
	// offered as a new default, since chat-time resolution would reject it.
	//
	// Unlike IsDefault this IS on the wire: "which one is bound" has a single
	// top-level answer, but "which of these can I pick" is a per-option fact
	// with no other carrier.
	Disabled bool `json:"disabled"`

	// EmptyTeam reports that this option is a TEAM with no enabled member.
	// Same consequence as Disabled — chat-time resolution drops the selection
	// and the standard path runs — but a separate field, because the two ask
	// the admin for different work: one is a switch to flip, the other is a
	// team to staff. Collapsing them into Disabled would have told an admin to
	// „einschalten" something that is already on.
	//
	// Never true for an agent: an agent has no membership. The projection is
	// deliberately teams-only rather than symmetric.
	EmptyTeam bool `json:"emptyTeam"`

	// IsDefault is how the source reports WHICH option is the current
	// binding, and it is deliberately not on the wire: AgentBindingInfo.ID
	// already names the bound option at the top level, and two places on one
	// payload that answer "what is bound?" is two places that can disagree
	// after a partial edit. The client compares option.id with the top-level
	// id.
	IsDefault bool `json:"-"`
}

// AgentBindingInfo is the workflow endpoint's full answer about the binding:
// what is bound right now, and what could be bound instead.
//
// It is a TOP-LEVEL field on ProjectedGraph rather than something hung off
// NodeAgentBinding, because Options and ID are values Project has no argument
// for and never reads. Putting them on the node would mean widening Project's
// input with data the projection ignores, and the same argument that kept ids
// off AgentBinding (see below) keeps them off the projection entirely. It sits
// next to PresetBase/Deviations, the other fields the handler annotates onto a
// graph Project cannot complete on its own.
//
// Kind ranges over all four BindingKind values, including BindingUnknown — see
// there for why an unresolved read must not report itself as "nothing bound".
// ID and Name are empty unless Kind is BindingAgent or BindingTeam.
//
// Disabled is a fourth state layered on Kind rather than a fifth BindingKind:
// the KB really does have an agent bound — that is what Kind/ID/Name say, and
// what the inspector needs in order to CLEAR it — it just does not take effect.
// Folding it into Kind would have forced every consumer that only asks "agent
// or team?" to learn two more values.
type AgentBindingInfo struct {
	Kind BindingKind `json:"kind"`
	ID   string      `json:"id"`
	Name string      `json:"name"`

	// Disabled: a default IS bound, and the agent/team it points at is
	// switched off. The row survives — and springs back to life the moment
	// anyone re-enables the agent — which is exactly why it must be visible
	// and clearable rather than filtered away.
	Disabled bool `json:"disabled"`

	// EmptyTeam: a TEAM is bound and it has no enabled member. Layered beside
	// Disabled for the same reason Disabled is layered beside Kind — the KB
	// really does have a team bound, which is what the inspector needs in order
	// to clear it, it just cannot answer anything. See BindingOption.EmptyTeam
	// for why this is not folded into Disabled.
	EmptyTeam bool `json:"emptyTeam"`

	Options []BindingOption `json:"options"`
}

// binding narrows the wire payload to the two fields Project consumes.
//
// This is the single place the two representations meet, which is what keeps
// them from disagreeing: the node's activation and the inspector's selection
// are derived from one resolved value, not read twice from the store.
func (i AgentBindingInfo) binding() AgentBinding {
	return AgentBinding{
		Kind: i.Kind, Name: i.Name, Disabled: i.Disabled, EmptyTeam: i.EmptyTeam,
	}
}

// bindingInfoFrom builds the wire payload from the attachable set, picking the
// option the source flagged as the KB default.
//
// One default per KB across BOTH link tables is enforced in the store
// (Store.attach clears every is_default on the KB in the same transaction), so
// the first flagged option is the only flagged option. Taking the first rather
// than asserting uniqueness is deliberate: if a hand-edited row ever broke that
// invariant, a canvas that named one of them still beats a canvas that errored.
//
// Options is always non-nil so the field marshals as [] rather than null.
func bindingInfoFrom(opts []BindingOption) AgentBindingInfo {
	info := AgentBindingInfo{Options: opts}
	if info.Options == nil {
		info.Options = []BindingOption{}
	}
	for _, o := range info.Options {
		if o.IsDefault {
			info.Kind, info.ID, info.Name = o.Kind, o.ID, o.Name
			info.Disabled, info.EmptyTeam = o.Disabled, o.EmptyTeam
			break
		}
	}
	return info
}

// AgentBinding is one KB's resolved default agent/team binding, as Project
// needs it.
//
// It is deliberately a LOCAL struct rather than an internal/agentteams type.
// internal/pipeline is a leaf package (see the package doc in nodes.go) and
// must not import internal/agentteams; the HTTP handler that already talks to
// the agentteams store maps between the two. That keeps the projection a pure
// function of values the caller resolved, testable without a database.
//
// It carries only what the projection consumes: Kind decides the node's
// activation and whether chat.OrchTeam becomes a candidate, Name is rendered
// into the node's German Condition. Ids are deliberately absent — nothing in
// Project reads one, and adding a field before its consumer exists is how this
// branch has previously shipped dead wire fields. The ids the inspector needs
// live on AgentBindingInfo/BindingOption, the handler-annotated wire payload,
// which is where their consumer is.
type AgentBinding struct {
	Kind BindingKind
	Name string

	// Disabled: the bound agent/team is switched off (is_enabled false). The
	// LINK still exists — Kind and Name still name it, and the node still has
	// to render it so an admin can clear it — but nothing routes through it.
	Disabled bool

	// EmptyTeam: the bound TEAM has no enabled member. The second way a link
	// row can exist and never fire, and the one the projection used to miss:
	// LoadTeamForChat returns zero members, resolveTeamSelection answers
	// (nil, "empty_team"), and the turn runs the FULL standard path — while the
	// canvas drew the node aktiv, projected chat.OrchTeam, and demoted every
	// PrepareChatContext-owned stage to bedingt on all three lanes.
	//
	// Teams-only by construction. An agent has no membership, so there is no
	// symmetric agent case to invent.
	EmptyTeam bool
}

// Bound reports whether the KB's default agent/team actually takes effect.
//
// It matches on the two real kinds rather than on `Kind != BindingNone`, so an
// unknown string that somehow reached this struct degrades to "nothing bound"
// instead of projecting an active node and an OrchTeam candidate for a binding
// nobody can name.
//
// A DISABLED binding is false here, and that is the honesty rule, not a
// convenience: chat-time resolution filters is_enabled (LoadAgentForChat /
// LoadTeamForChat in internal/agentteams, and the web client's own list read
// before them), so a turn on such a KB never reaches chat.OrchTeam and the
// standard path really does run. Every consumer of Bound() asks "does the
// binding change what runs?" — orchestratorCandidates, demoteForBinding,
// Project's PrepareChatContext-bypass rule — and for a disabled binding the
// answer to all three is no. The fact that a ROW exists is carried by Kind +
// Disabled, which is what applyAgentBinding and the inspector read instead.
//
// An EMPTY TEAM is false for the identical reason, one step further along:
// LoadTeamForChat's member query filters is_enabled too, so a team with no
// enabled member resolves to zero members, resolveTeamSelection returns
// (nil, "empty_team"), and the turn runs the full standard path. A team can
// reach that state by having its members switched off or by being created with
// an empty memberIds — the handler accepts one. Nothing about the team itself
// says so: is_enabled is true and the link row is real.
func (b AgentBinding) Bound() bool {
	return !b.Disabled && !b.EmptyTeam &&
		(b.Kind == BindingAgent || b.Kind == BindingTeam)
}
