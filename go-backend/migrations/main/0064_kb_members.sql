-- +goose Up
-- Vier KB-Rollen in einer Tabelle. Ersetzt fachlich knowledge_base_shares
-- (view/edit), global_kb_editors (admin) und knowledge_bases.user_id (owner)
-- als Autoritaetsquelle. Gelesen von internal/kbaccess (Zugriffsaufloesung)
-- und internal/kbmembers (Mitgliederverwaltung).
-- Die beiden Alttabellen bleiben in diesem Release bestehen (expand/contract)
-- und werden erst nach Phase 2 entfernt.
CREATE TABLE IF NOT EXISTS kb_members (
    kb_id      uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       varchar(10) NOT NULL CHECK (role IN ('view','edit','admin','owner')),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    PRIMARY KEY (kb_id, user_id)
);

-- Genau ein Owner pro KB. Partiell, damit beliebig viele view/edit/admin
-- nebeneinander stehen koennen.
CREATE UNIQUE INDEX IF NOT EXISTS kb_members_owner_uniq
    ON kb_members (kb_id) WHERE role = 'owner';

-- "Welche KBs sieht Nutzer X" (kb.ListKnowledgeBases) scannt nach user_id.
CREATE INDEX IF NOT EXISTS kb_members_user_idx ON kb_members (user_id);

-- Backfill in Prioritaetsreihenfolge: Owner gewinnt gegen Share gewinnt gegen
-- Global-Editor. ON CONFLICT DO NOTHING setzt das durch, weil die
-- hoeherwertige Zeile zuerst geschrieben wird.
INSERT INTO kb_members (kb_id, user_id, role)
SELECT id, user_id, 'owner' FROM knowledge_bases WHERE user_id IS NOT NULL
ON CONFLICT (kb_id, user_id) DO NOTHING;

INSERT INTO kb_members (kb_id, user_id, role)
SELECT kb_id, user_id, permission FROM knowledge_base_shares
WHERE permission IN ('view','edit')
ON CONFLICT (kb_id, user_id) DO NOTHING;

-- Global-Editoren werden 'admin', nicht 'edit': sie sind die bestellten
-- Kuratoren ihrer KB und koennten sonst nach der Umstellung keine
-- Einstellungen mehr aendern.
INSERT INTO kb_members (kb_id, user_id, role)
SELECT kb_id, user_id, 'admin' FROM global_kb_editors
ON CONFLICT (kb_id, user_id) DO NOTHING;

-- knowledge_bases.user_id bleibt als denormalisierter Spiegel bestehen: rund
-- 40 bestehende Queries joinen darueber den Owner-Namen. Der Trigger haelt ihn
-- synchron. Nur INSERT/UPDATE — eine Owner-Zeile wird nie einzeln geloescht
-- (der Owner kann nicht verlassen, ein Transfer aktualisiert die Zeile, und
-- beim Loeschen von KB oder Nutzer verschwindet die KB-Zeile ohnehin mit).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION kb_members_sync_owner() RETURNS trigger AS $$
BEGIN
    UPDATE knowledge_bases SET user_id = NEW.user_id WHERE id = NEW.kb_id;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS kb_members_sync_owner_trg ON kb_members;
CREATE TRIGGER kb_members_sync_owner_trg
    AFTER INSERT OR UPDATE ON kb_members
    FOR EACH ROW WHEN (NEW.role = 'owner')
    EXECUTE FUNCTION kb_members_sync_owner();

-- pending_kb_invites parkt Einladungen fuer noch nicht angelegte OIDC-Nutzer.
-- 'permission' wird zu 'role', weil jetzt auch 'admin' einladbar ist.
-- RENAME COLUMN ist nicht idempotent, deshalb per information_schema-Guard:
-- auf einer frischen DB laeuft dieser Block direkt nach der obigen
-- CREATE TABLE-Sektion (Spalte heisst noch 'permission'), auf einer DB, auf
-- der 0064 schon einmal angewendet wurde, muss der zweite Lauf tolerant sein
-- (Spalte heisst schon 'role', IF EXISTS greift nicht).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'pending_kb_invites' AND column_name = 'permission') THEN
        ALTER TABLE pending_kb_invites RENAME COLUMN permission TO role;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE pending_kb_invites DROP CONSTRAINT IF EXISTS pending_kb_invites_role_check;
ALTER TABLE pending_kb_invites
    ADD CONSTRAINT pending_kb_invites_role_check CHECK (role IN ('view','edit','admin'));

-- +goose Down
DROP TRIGGER IF EXISTS kb_members_sync_owner_trg ON kb_members;
DROP FUNCTION IF EXISTS kb_members_sync_owner();
DROP TABLE IF EXISTS kb_members;
