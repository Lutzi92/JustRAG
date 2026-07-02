-- +goose Up
-- User-created agents (specialists) and agent teams (router + specialists).
-- Agents are user-owned; kb_id is reserved for Phase-3 cross-KB pinning and
-- stays NULL in v1. config holds per-agent retrieval-knob overrides using the
-- kb_site_configs key vocabulary (allowlisted via siteconfig.IsPerAgent).
CREATE TABLE IF NOT EXISTS agents (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          VARCHAR(100) NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    icon          VARCHAR(64) NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    chat_model    VARCHAR(200) NOT NULL DEFAULT '',
    tool_names    TEXT[] NOT NULL DEFAULT '{}',
    config        JSONB NOT NULL DEFAULT '{}',
    kb_id         UUID REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    is_enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMP NOT NULL DEFAULT now(),
    updated_at    TIMESTAMP NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agents_user_id_idx ON agents(user_id);

CREATE TABLE IF NOT EXISTS agent_teams (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                VARCHAR(100) NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    icon                VARCHAR(64) NOT NULL DEFAULT '',
    max_agents_per_turn INTEGER NOT NULL DEFAULT 3,
    is_enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMP NOT NULL DEFAULT now(),
    updated_at          TIMESTAMP NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_teams_user_id_idx ON agent_teams(user_id);

CREATE TABLE IF NOT EXISTS agent_team_members (
    team_id  UUID NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, agent_id)
);

CREATE TABLE IF NOT EXISTS agent_kb_links (
    agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    kb_id      UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    is_default BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (agent_id, kb_id)
);
CREATE INDEX IF NOT EXISTS agent_kb_links_kb_id_idx ON agent_kb_links(kb_id);

CREATE TABLE IF NOT EXISTS team_kb_links (
    team_id    UUID NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    kb_id      UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    is_default BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (team_id, kb_id)
);
CREATE INDEX IF NOT EXISTS team_kb_links_kb_id_idx ON team_kb_links(kb_id);

-- Sticky per-chat selection. SET NULL so deleting an agent/team never
-- deletes chats.
ALTER TABLE chats ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES agent_teams(id) ON DELETE SET NULL;
ALTER TABLE chats ADD COLUMN IF NOT EXISTS agent_id UUID REFERENCES agents(id) ON DELETE SET NULL;

-- Telemetry slicing for the admin metrics panel (mode='team' rows).
ALTER TABLE agent_decisions ADD COLUMN IF NOT EXISTS team_id UUID;
ALTER TABLE agent_decisions ADD COLUMN IF NOT EXISTS agent_id UUID;

-- +goose Down
ALTER TABLE agent_decisions DROP COLUMN IF EXISTS agent_id;
ALTER TABLE agent_decisions DROP COLUMN IF EXISTS team_id;
ALTER TABLE chats DROP COLUMN IF EXISTS agent_id;
ALTER TABLE chats DROP COLUMN IF EXISTS team_id;
DROP TABLE IF EXISTS team_kb_links;
DROP TABLE IF EXISTS agent_kb_links;
DROP TABLE IF EXISTS agent_team_members;
DROP TABLE IF EXISTS agent_teams;
DROP TABLE IF EXISTS agents;
