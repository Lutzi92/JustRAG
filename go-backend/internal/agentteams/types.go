// Package agentteams implements user-created agents (specialist personas with
// tool allowlists and retrieval-knob overrides) and agent teams (a named group
// routed by an LLM router). Storage + HTTP CRUD live here; the RunTeamChat
// orchestrator that executes a team lives in internal/chat (which imports this
// package for the record types — agentteams must never import chat).
package agentteams

import "time"

// AgentRecord is one user-owned specialist agent.
type AgentRecord struct {
	ID           string            `json:"id" db:"id"`
	UserID       string            `json:"userId" db:"user_id"`
	Name         string            `json:"name" db:"name"`
	Description  string            `json:"description" db:"description"`
	Icon         string            `json:"icon" db:"icon"`
	SystemPrompt string            `json:"systemPrompt" db:"system_prompt"`
	ChatModel    string            `json:"chatModel" db:"chat_model"`
	ToolNames    []string          `json:"toolNames" db:"tool_names"`
	Config       map[string]string `json:"config" db:"config"`
	IsEnabled    bool              `json:"isEnabled" db:"is_enabled"`
	CreatedAt    time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt    time.Time         `json:"updatedAt" db:"updated_at"`
}

// TeamRecord is one user-owned team; MemberIDs is loaded via array_agg.
type TeamRecord struct {
	ID               string    `json:"id" db:"id"`
	UserID           string    `json:"userId" db:"user_id"`
	Name             string    `json:"name" db:"name"`
	Description      string    `json:"description" db:"description"`
	Icon             string    `json:"icon" db:"icon"`
	MaxAgentsPerTurn int       `json:"maxAgentsPerTurn" db:"max_agents_per_turn"`
	IsEnabled        bool      `json:"isEnabled" db:"is_enabled"`
	MemberIDs        []string  `json:"memberIds" db:"member_ids"`
	CreatedAt        time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt        time.Time `json:"updatedAt" db:"updated_at"`
}

// TeamForChat is the use-time load: the team plus its enabled member agents,
// verified attached to the requesting chat's KB.
type TeamForChat struct {
	Team    TeamRecord
	Members []AgentRecord
}

// AttachedAgent / AttachedTeam are the picker DTOs for GET /api/kb/{id}/agents.
type AttachedAgent struct {
	ID          string `json:"id" db:"id"`
	Name        string `json:"name" db:"name"`
	Description string `json:"description" db:"description"`
	Icon        string `json:"icon" db:"icon"`
	IsDefault   bool   `json:"isDefault" db:"is_default"`
}

type AttachedTeam struct {
	ID          string `json:"id" db:"id"`
	Name        string `json:"name" db:"name"`
	Description string `json:"description" db:"description"`
	Icon        string `json:"icon" db:"icon"`
	IsDefault   bool   `json:"isDefault" db:"is_default"`
	MemberCount int    `json:"memberCount" db:"member_count"`
}

// KBAgents is the full picker payload.
type KBAgents struct {
	Agents []AttachedAgent `json:"agents"`
	Teams  []AttachedTeam  `json:"teams"`
}

// BindingCandidate is one agent or team attached to a KB, as the WORKFLOW
// canvas needs to see it — which is deliberately NOT what the chat picker sees.
//
// The picker (AttachedAgent/AttachedTeam via ListAttachedForKB) filters
// is_enabled away, and must keep doing so: a disabled agent is not selectable
// in chat, because chat-time resolution (LoadAgentForChat / LoadTeamForChat)
// rejects it too. But a KB whose agent_kb_links.is_default row points at a
// DISABLED agent still has that row, and an admin has to be able to see and
// clear it. Filtering it out made the canvas project „keine Vorgabe" on a KB
// that has one, with no way to remove it and no warning that re-enabling the
// agent would bring it silently back.
//
// So this is a second read, not a widened first one: ListAttachedForKB's
// contract is unchanged, and there is no parameter whose wrong default would
// leak a disabled agent into the picker.
//
// IsEnabled is carried rather than filtered on, because "bound but switched
// off" is a state the canvas must render, not one it may drop.
type BindingCandidate struct {
	// Kind is "agent" or "team", selected as a SQL literal by whichever of the
	// two queries produced the row — pgx.RowToStructByName demands a column per
	// exported field, so it cannot be stamped on afterwards in Go.
	Kind      string `db:"kind"`
	ID        string `db:"id"`
	Name      string `db:"name"`
	IsDefault bool   `db:"is_default"`
	IsEnabled bool   `db:"is_enabled"`

	// EnabledMemberCount is how many ENABLED agents the team has. It is the
	// second reason a binding can exist and never fire, and it was the one the
	// canvas could not see: is_enabled alone made a team with zero usable
	// members look live.
	//
	// It mirrors exactly what chat-time resolution does. LoadTeamForChat's
	// member query carries `AND is_enabled`, and resolveTeamSelection
	// (internal/chat/http_send.go) turns `len(members) == 0` into
	// (nil, "empty_team") — the selection is dropped and the full standard path
	// runs. CreateTeam accepts an empty memberIds, so a team can also be born
	// this way, not only disabled into it.
	//
	// Always 1 for an AGENT row, and that is a deliberate not-applicable
	// encoding rather than a fact: an agent has no membership, so nothing about
	// members can stop it from running, and a zero here would make every agent
	// binding look empty. The team query is the only one that computes it.
	EnabledMemberCount int `db:"enabled_member_count"`
}
