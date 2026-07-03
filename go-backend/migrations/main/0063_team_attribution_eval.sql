-- +goose Up
-- Message-level team/agent attribution (Phase-2 of agent teams; closes
-- Phase-1 spec deviation #1): which team/agent answered each AI message.
-- SET NULL so deleting an agent/team never deletes messages.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES agent_teams(id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS agent_id UUID REFERENCES agents(id) ON DELETE SET NULL;

-- Per-team eval runs (admineval async path): which team an eval run
-- dispatched through. NULL = standard run (byte-stable legacy behavior).
ALTER TABLE eval_runs ADD COLUMN IF NOT EXISTS team_id UUID REFERENCES agent_teams(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE eval_runs DROP COLUMN IF EXISTS team_id;
ALTER TABLE messages DROP COLUMN IF EXISTS agent_id;
ALTER TABLE messages DROP COLUMN IF EXISTS team_id;
