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
