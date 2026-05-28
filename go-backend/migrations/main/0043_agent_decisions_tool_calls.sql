-- +goose Up
-- AP-B4 admin agent-metrics: per-turn tool-call sequence so the admin
-- "Tool-Mix (live)" card can show which tools the AP-B3 tool-aware
-- planner actually picked. JSONB array of {tool, duration_ms, status}
-- with status ∈ {ok, error, timeout}. Default '[]' so old rows keep
-- a valid (if empty) shape — frontend never has to nil-check.
ALTER TABLE "agent_decisions"
    ADD COLUMN IF NOT EXISTS "tool_calls" JSONB NOT NULL DEFAULT '[]'::jsonb;

-- A GIN index on tool_calls would help "show me all turns that used
-- code_exec", but the current admin panel only aggregates counts —
-- the existing (kb_id, created_at) btree index already covers the
-- aggregate path. Skip the index until the panel grows a per-tool
-- drill-down.

-- +goose Down
ALTER TABLE "agent_decisions" DROP COLUMN IF EXISTS "tool_calls";
