package agentteams

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// ErrNotFound is returned when a record does not exist (or is not visible to
// the caller — GetAgent/GetTeam scope by owner in SQL).
var ErrNotFound = errors.New("agentteams: not found")

// Store is the Postgres accessor for agents, teams, and KB links.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const agentCols = `id, user_id, name, description, icon, system_prompt,
	chat_model, tool_names, config, is_enabled, created_at, updated_at`

const teamCols = `t.id, t.user_id, t.name, t.description, t.icon,
	t.max_agents_per_turn, t.is_enabled,
	COALESCE((SELECT array_agg(m.agent_id::text ORDER BY m.agent_id)
	          FROM agent_team_members m WHERE m.team_id = t.id), '{}') AS member_ids,
	t.created_at, t.updated_at`

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

func (s *Store) CreateAgent(ctx context.Context, a AgentRecord) (*AgentRecord, error) {
	sql := `INSERT INTO agents (user_id, name, description, icon, system_prompt,
	          chat_model, tool_names, config, is_enabled)
	        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	        RETURNING ` + agentCols
	row, err := pgxutil.QueryOne[AgentRecord](ctx, s.pool, sql,
		a.UserID, a.Name, a.Description, a.Icon, a.SystemPrompt,
		a.ChatModel, a.ToolNames, a.Config, a.IsEnabled)
	if err != nil {
		return nil, fmt.Errorf("CreateAgent: %w", err)
	}
	return row, nil
}

func (s *Store) ListAgentsByUser(ctx context.Context, userID string) ([]AgentRecord, error) {
	sql := `SELECT ` + agentCols + ` FROM agents WHERE user_id = $1 ORDER BY name`
	rows, err := pgxutil.QueryRows[AgentRecord](ctx, s.pool, sql, userID)
	if err != nil {
		return nil, fmt.Errorf("ListAgentsByUser: %w", err)
	}
	return rows, nil
}

// GetAgent is owner-scoped: a foreign id behaves like a missing one.
func (s *Store) GetAgent(ctx context.Context, id, userID string) (*AgentRecord, error) {
	sql := `SELECT ` + agentCols + ` FROM agents WHERE id = $1 AND user_id = $2`
	row, err := pgxutil.QueryOne[AgentRecord](ctx, s.pool, sql, id, userID)
	if err != nil {
		return nil, fmt.Errorf("GetAgent: %w", err)
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

func (s *Store) UpdateAgent(ctx context.Context, a AgentRecord) (*AgentRecord, error) {
	sql := `UPDATE agents SET name=$3, description=$4, icon=$5, system_prompt=$6,
	          chat_model=$7, tool_names=$8, config=$9, is_enabled=$10, updated_at=now()
	        WHERE id = $1 AND user_id = $2
	        RETURNING ` + agentCols
	row, err := pgxutil.QueryOne[AgentRecord](ctx, s.pool, sql,
		a.ID, a.UserID, a.Name, a.Description, a.Icon, a.SystemPrompt,
		a.ChatModel, a.ToolNames, a.Config, a.IsEnabled)
	if err != nil {
		return nil, fmt.Errorf("UpdateAgent: %w", err)
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

func (s *Store) DeleteAgent(ctx context.Context, id, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return false, fmt.Errorf("DeleteAgent: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// CountOwnedAgents returns how many of ids exist AND belong to userID —
// the team-membership same-owner check.
func (s *Store) CountOwnedAgents(ctx context.Context, userID string, ids []string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE user_id = $1 AND id = ANY($2::uuid[])`,
		userID, ids).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("CountOwnedAgents: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Teams
// ---------------------------------------------------------------------------

func (s *Store) CreateTeam(ctx context.Context, tm TeamRecord) (*TeamRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("CreateTeam: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO agent_teams (user_id, name, description, icon, max_agents_per_turn, is_enabled)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		tm.UserID, tm.Name, tm.Description, tm.Icon, tm.MaxAgentsPerTurn, tm.IsEnabled).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("CreateTeam: insert: %w", err)
	}
	if err := replaceMembersTx(ctx, tx, id, tm.MemberIDs); err != nil {
		return nil, fmt.Errorf("CreateTeam: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("CreateTeam: commit: %w", err)
	}
	return s.GetTeam(ctx, id, tm.UserID)
}

func (s *Store) ListTeamsByUser(ctx context.Context, userID string) ([]TeamRecord, error) {
	sql := `SELECT ` + teamCols + ` FROM agent_teams t WHERE t.user_id = $1 ORDER BY t.name`
	rows, err := pgxutil.QueryRows[TeamRecord](ctx, s.pool, sql, userID)
	if err != nil {
		return nil, fmt.Errorf("ListTeamsByUser: %w", err)
	}
	return rows, nil
}

func (s *Store) GetTeam(ctx context.Context, id, userID string) (*TeamRecord, error) {
	sql := `SELECT ` + teamCols + ` FROM agent_teams t WHERE t.id = $1 AND t.user_id = $2`
	row, err := pgxutil.QueryOne[TeamRecord](ctx, s.pool, sql, id, userID)
	if err != nil {
		return nil, fmt.Errorf("GetTeam: %w", err)
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

func (s *Store) UpdateTeam(ctx context.Context, tm TeamRecord) (*TeamRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("UpdateTeam: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE agent_teams SET name=$3, description=$4, icon=$5,
		   max_agents_per_turn=$6, is_enabled=$7, updated_at=now()
		 WHERE id = $1 AND user_id = $2`,
		tm.ID, tm.UserID, tm.Name, tm.Description, tm.Icon, tm.MaxAgentsPerTurn, tm.IsEnabled)
	if err != nil {
		return nil, fmt.Errorf("UpdateTeam: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if err := replaceMembersTx(ctx, tx, tm.ID, tm.MemberIDs); err != nil {
		return nil, fmt.Errorf("UpdateTeam: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("UpdateTeam: commit: %w", err)
	}
	return s.GetTeam(ctx, tm.ID, tm.UserID)
}

func (s *Store) DeleteTeam(ctx context.Context, id, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_teams WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return false, fmt.Errorf("DeleteTeam: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func replaceMembersTx(ctx context.Context, tx pgx.Tx, teamID string, memberIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM agent_team_members WHERE team_id = $1`, teamID); err != nil {
		return fmt.Errorf("replace members: delete: %w", err)
	}
	if len(memberIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO agent_team_members (team_id, agent_id)
		 SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, teamID, memberIDs)
	if err != nil {
		return fmt.Errorf("replace members: insert: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// KB attachment
// ---------------------------------------------------------------------------

// AttachAgent links agentID to kbID. isDefault clears every other default on
// the KB (across BOTH link tables — one default per KB) in the same tx.
func (s *Store) AttachAgent(ctx context.Context, kbID, agentID string, isDefault bool) error {
	return s.attach(ctx, kbID, agentID, isDefault,
		`INSERT INTO agent_kb_links (agent_id, kb_id, is_default) VALUES ($1, $2, $3)
		 ON CONFLICT (agent_id, kb_id) DO UPDATE SET is_default = EXCLUDED.is_default`)
}

func (s *Store) AttachTeam(ctx context.Context, kbID, teamID string, isDefault bool) error {
	return s.attach(ctx, kbID, teamID, isDefault,
		`INSERT INTO team_kb_links (team_id, kb_id, is_default) VALUES ($1, $2, $3)
		 ON CONFLICT (team_id, kb_id) DO UPDATE SET is_default = EXCLUDED.is_default`)
}

func (s *Store) attach(ctx context.Context, kbID, id string, isDefault bool, insertSQL string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attach: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if isDefault {
		if _, err := tx.Exec(ctx, `UPDATE agent_kb_links SET is_default = false WHERE kb_id = $1`, kbID); err != nil {
			return fmt.Errorf("attach: clear agent defaults: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE team_kb_links SET is_default = false WHERE kb_id = $1`, kbID); err != nil {
			return fmt.Errorf("attach: clear team defaults: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, insertSQL, id, kbID, isDefault); err != nil {
		return fmt.Errorf("attach: insert: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) DetachAgent(ctx context.Context, kbID, agentID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_kb_links WHERE kb_id = $1 AND agent_id = $2`, kbID, agentID)
	if err != nil {
		return false, fmt.Errorf("DetachAgent: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) DetachTeam(ctx context.Context, kbID, teamID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM team_kb_links WHERE kb_id = $1 AND team_id = $2`, kbID, teamID)
	if err != nil {
		return false, fmt.Errorf("DetachTeam: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListAttachedForKB returns the picker payload: enabled agents + teams
// attached to the KB. Anyone with KB view may call this (route-gated).
func (s *Store) ListAttachedForKB(ctx context.Context, kbID string) (*KBAgents, error) {
	agents, err := pgxutil.QueryRows[AttachedAgent](ctx, s.pool,
		`SELECT a.id, a.name, a.description, a.icon, l.is_default
		 FROM agent_kb_links l JOIN agents a ON a.id = l.agent_id
		 WHERE l.kb_id = $1 AND a.is_enabled ORDER BY a.name`, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListAttachedForKB: agents: %w", err)
	}
	teams, err := pgxutil.QueryRows[AttachedTeam](ctx, s.pool,
		`SELECT t.id, t.name, t.description, t.icon, l.is_default,
		        (SELECT count(*) FROM agent_team_members m WHERE m.team_id = t.id)::int AS member_count
		 FROM team_kb_links l JOIN agent_teams t ON t.id = l.team_id
		 WHERE l.kb_id = $1 AND t.is_enabled ORDER BY t.name`, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListAttachedForKB: teams: %w", err)
	}
	if agents == nil {
		agents = []AttachedAgent{}
	}
	if teams == nil {
		teams = []AttachedTeam{}
	}
	return &KBAgents{Agents: agents, Teams: teams}, nil
}

// ---------------------------------------------------------------------------
// Chat-time loads (use-time authorization: must be attached to THIS kb + enabled)
// ---------------------------------------------------------------------------

func (s *Store) LoadTeamForChat(ctx context.Context, teamID, kbID string) (*TeamForChat, error) {
	team, err := pgxutil.QueryOne[TeamRecord](ctx, s.pool,
		`SELECT `+teamCols+` FROM agent_teams t
		 JOIN team_kb_links l ON l.team_id = t.id AND l.kb_id = $2
		 WHERE t.id = $1 AND t.is_enabled`, teamID, kbID)
	if err != nil {
		return nil, fmt.Errorf("LoadTeamForChat: %w", err)
	}
	if team == nil {
		return nil, ErrNotFound
	}
	members, err := pgxutil.QueryRows[AgentRecord](ctx, s.pool,
		`SELECT `+agentCols+` FROM agents
		 WHERE id IN (SELECT agent_id FROM agent_team_members WHERE team_id = $1)
		   AND is_enabled ORDER BY name`, teamID)
	if err != nil {
		return nil, fmt.Errorf("LoadTeamForChat: members: %w", err)
	}
	return &TeamForChat{Team: *team, Members: members}, nil
}

func (s *Store) LoadAgentForChat(ctx context.Context, agentID, kbID string) (*AgentRecord, error) {
	row, err := pgxutil.QueryOne[AgentRecord](ctx, s.pool,
		`SELECT a.id, a.user_id, a.name, a.description, a.icon, a.system_prompt,
		        a.chat_model, a.tool_names, a.config, a.is_enabled, a.created_at, a.updated_at
		 FROM agents a
		 JOIN agent_kb_links l ON l.agent_id = a.id AND l.kb_id = $2
		 WHERE a.id = $1 AND a.is_enabled`, agentID, kbID)
	if err != nil {
		return nil, fmt.Errorf("LoadAgentForChat: %w", err)
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

// ModelExists reports whether name is one of the deployment's configured
// models (create-time validation of AgentRecord.ChatModel).
func (s *Store) ModelExists(ctx context.Context, name string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ai_models WHERE name = $1)`, name).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("ModelExists: %w", err)
	}
	return ok, nil
}
