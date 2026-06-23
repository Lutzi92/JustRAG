-- +goose Up
-- Bulk-invite for not-yet-logged-in users. OIDC users are created in the DB only
-- on first login, so a share row (FK to users.id) cannot exist for them yet. We
-- park the invite here keyed on the username they'll be provisioned with, and
-- promote it to a real knowledge_base_shares row on their first OIDC login
-- (authhandler.ApplyPendingInvites). No FK to users by design — the user doesn't
-- exist yet. Cascades with the KB so invites die with the knowledge base.
CREATE TABLE IF NOT EXISTS pending_kb_invites (
    id         UUID NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    kb_id      UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    username   TEXT NOT NULL,
    permission VARCHAR(10) NOT NULL DEFAULT 'view',
    invited_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotent re-invite: (kb_id, lower(username)) is unique so a repeated paste
-- upserts instead of duplicating. Also the lookup index used at login time.
CREATE UNIQUE INDEX IF NOT EXISTS pending_kb_invites_kb_user_idx
    ON pending_kb_invites (kb_id, LOWER(username));

-- Login-time promotion scans by username across all KBs.
CREATE INDEX IF NOT EXISTS pending_kb_invites_username_idx
    ON pending_kb_invites (LOWER(username));

-- +goose Down
DROP INDEX IF EXISTS pending_kb_invites_username_idx;
DROP INDEX IF EXISTS pending_kb_invites_kb_user_idx;
DROP TABLE IF EXISTS pending_kb_invites;
