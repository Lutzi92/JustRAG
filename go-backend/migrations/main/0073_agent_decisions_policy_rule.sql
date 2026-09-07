-- +goose Up
-- Wave 6 (W6-R6): which chat_orchestrator_policy rule (0-based index) pinned
-- this turn's orchestrator. NULL = the flag ladder decided (no rule matched,
-- a "prefer" rule's flag was off, or the policy is empty). No backfill.
ALTER TABLE agent_decisions ADD COLUMN IF NOT EXISTS policy_rule smallint;

-- +goose Down
ALTER TABLE agent_decisions DROP COLUMN IF EXISTS policy_rule;
