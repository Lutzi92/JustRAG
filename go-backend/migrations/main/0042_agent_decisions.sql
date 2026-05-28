-- +goose Up
-- Phase 1 §1.4 admin agent-metrics panel: lightweight per-chat decision log.
-- Insert is fire-and-forget at the end of each chat run so the panel can
-- aggregate outcome distributions / median hops / p95 latency without
-- depending on Prometheus retention.
CREATE TABLE IF NOT EXISTS "agent_decisions" (
    "id"          BIGSERIAL PRIMARY KEY,
    "created_at"  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    "kb_id"       UUID NOT NULL,
    "mode"        TEXT NOT NULL,            -- 'agentic' | 'plan_execute' | 'crag' | 'standard'
    "outcome"     TEXT NOT NULL,            -- per-mode outcome label (closed enum, mirrors metrics)
    "hops"        INTEGER NOT NULL DEFAULT 0,
    "rounds"      INTEGER NOT NULL DEFAULT 0,
    "latency_ms"  INTEGER NOT NULL DEFAULT 0
);

-- Compound index to keep the per-window per-KB queries the admin panel
-- issues (`WHERE kb_id = ? AND created_at >= ?`) on a single index scan.
-- BRIN would be a better fit at petabyte scale but a B-tree on a write-
-- light table aged-out by an external sweep is the right starting point.
CREATE INDEX IF NOT EXISTS "agent_decisions_kb_created_idx"
    ON "agent_decisions" ("kb_id", "created_at" DESC);

-- +goose Down
DROP INDEX IF EXISTS "agent_decisions_kb_created_idx";
DROP TABLE IF EXISTS "agent_decisions";
