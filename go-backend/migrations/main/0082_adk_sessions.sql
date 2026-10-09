-- +goose Up
-- ADK (google.golang.org/adk/v2) session store for agentic chat and user
-- workflows (docs/superpowers/specs/2026-10-08-adk-go-adoption-plan.md).
-- Events are stored whole as JSONB: session.Event round-trips through JSON,
-- so an ADK release that adds an event field needs no migration.
-- user_id is text, not a users FK: ADK keys sessions by an opaque string,
-- and the app layer passes the JustRAG user id. Cleanup on user deletion is
-- done by internal/cascade (Phase 2), not by FK.
CREATE TABLE IF NOT EXISTS adk_sessions (
    app_name    text        NOT NULL,
    user_id     text        NOT NULL,
    id          text        NOT NULL,
    state       jsonb       NOT NULL DEFAULT '{}',
    create_time timestamptz NOT NULL DEFAULT now(),
    update_time timestamptz NOT NULL,
    PRIMARY KEY (app_name, user_id, id)
);
CREATE TABLE IF NOT EXISTS adk_events (
    app_name   text        NOT NULL,
    user_id    text        NOT NULL,
    session_id text        NOT NULL,
    id         text        NOT NULL,
    ts         timestamptz NOT NULL,
    body       jsonb       NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id, id),
    FOREIGN KEY (app_name, user_id, session_id) REFERENCES adk_sessions (app_name, user_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS adk_events_session_ts_idx ON adk_events (app_name, user_id, session_id, ts DESC, id DESC);
CREATE TABLE IF NOT EXISTS adk_app_states (
    app_name    text        PRIMARY KEY,
    state       jsonb       NOT NULL DEFAULT '{}',
    update_time timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS adk_user_states (
    app_name    text        NOT NULL,
    user_id     text        NOT NULL,
    state       jsonb       NOT NULL DEFAULT '{}',
    update_time timestamptz NOT NULL,
    PRIMARY KEY (app_name, user_id)
);

-- +goose Down
DROP TABLE IF EXISTS adk_events;
DROP TABLE IF EXISTS adk_sessions;
DROP TABLE IF EXISTS adk_user_states;
DROP TABLE IF EXISTS adk_app_states;
