-- +goose Up
-- Dauerhafte, widerrufbare Einladungslinks pro KB
-- (docs/superpowers/specs/2026-08-18-kb-invite-links-design.md).
-- Der Token liegt im Klartext, damit ein Link spaeter erneut kopiert werden
-- kann; 32 Byte Entropie machen Raten aussichtslos, das Rate-Limit auf
-- POST /api/invites/{token}/redeem ist die zweite Verteidigungslinie.
CREATE TABLE IF NOT EXISTS kb_invite_links (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id            UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    token            TEXT NOT NULL,
    role             TEXT NOT NULL,
    label            TEXT,
    created_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    redemption_count INTEGER NOT NULL DEFAULT 0,
    last_used_at     TIMESTAMPTZ
);

-- Spiegelt kbaccess.Assignable: 'owner' ist nicht darstellbar, auch nicht
-- ueber einen direkten DB-Schreibzugriff.
ALTER TABLE kb_invite_links DROP CONSTRAINT IF EXISTS kb_invite_links_role_check;
ALTER TABLE kb_invite_links
    ADD CONSTRAINT kb_invite_links_role_check CHECK (role IN ('view','edit','admin'));

CREATE UNIQUE INDEX IF NOT EXISTS kb_invite_links_token_idx ON kb_invite_links (token);
CREATE INDEX IF NOT EXISTS kb_invite_links_kb_idx ON kb_invite_links (kb_id);

-- +goose Down
DROP INDEX IF EXISTS kb_invite_links_kb_idx;
DROP INDEX IF EXISTS kb_invite_links_token_idx;
DROP TABLE IF EXISTS kb_invite_links;
