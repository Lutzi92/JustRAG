-- +goose Up
-- Phase 2 des KB-Rollenmodells (docs/superpowers/specs/2026-08-12-kb-rollen-
-- und-sichtbarkeit-design.md): visibility loest is_global als Schreibwahrheit
-- ab, auto_subscribe steuert das ungefragte Einblenden, kb_subscriptions und
-- kb_categories tragen Abo und Katalog.
ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS visibility varchar(10) NOT NULL DEFAULT 'private';

ALTER TABLE knowledge_bases DROP CONSTRAINT IF EXISTS knowledge_bases_visibility_check;
ALTER TABLE knowledge_bases
    ADD CONSTRAINT knowledge_bases_visibility_check CHECK (visibility IN ('private','public'));

-- DEFAULT false ist tragend, nicht beilaeufig: eine NEU veroeffentlichte KB
-- taucht im Katalog auf, aber in niemandes Uebersicht. Der Backfill unten
-- setzt das Flag nur fuer die heute schon published globalen KBs, damit am
-- Deployment-Tag niemand etwas verliert.
ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS auto_subscribe boolean NOT NULL DEFAULT false;

-- Backfill und Spaltentausch laufen nur beim ersten Durchlauf. Danach ist
-- is_global generiert, die UPDATEs waeren No-Ops und der Spaltentausch wuerde
-- die Tabelle sinnlos neu schreiben. is_generated = 'NEVER' unterscheidet
-- beide Zustaende zuverlaessig.
--
-- is_global bleibt als generierte Spalte stehen, damit Pods des vorherigen
-- Images das Rolling-Deployment ueberleben (unter k8s laufen die Migrationen
-- laut docs/runbooks/release.md von Hand VOR kubectl apply). Der Code von
-- Phase 2 liest sie nicht mehr; sie faellt im Cleanup-Release.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'knowledge_bases'
                 AND column_name = 'is_global'
                 AND is_generated = 'NEVER') THEN
        UPDATE knowledge_bases SET visibility = 'public' WHERE is_global;
        UPDATE knowledge_bases SET auto_subscribe = true WHERE is_global AND is_published;

        ALTER TABLE knowledge_bases DROP COLUMN is_global;
        ALTER TABLE knowledge_bases ADD COLUMN is_global boolean
            GENERATED ALWAYS AS (visibility = 'public') STORED;
    END IF;
END $$;
-- +goose StatementEnd

-- Abo-Zustand. Eine Zeile existiert nur, wenn der Nutzer aktiv entschieden
-- hat; auto_subscribe wirkt fuer alle ohne Zeile.
CREATE TABLE IF NOT EXISTS kb_subscriptions (
    kb_id      uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    state      varchar(12) NOT NULL CHECK (state IN ('subscribed','opted_out')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kb_id, user_id)
);

-- "Welche oeffentlichen KBs sieht Nutzer X" scannt nach user_id.
CREATE INDEX IF NOT EXISTS kb_subscriptions_user_idx ON kb_subscriptions (user_id);

CREATE TABLE IF NOT EXISTS kb_categories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       varchar(100) NOT NULL UNIQUE,
    sort_order int NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS kb_category_links (
    kb_id       uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    category_id uuid NOT NULL REFERENCES kb_categories(id) ON DELETE CASCADE,
    PRIMARY KEY (kb_id, category_id)
);

-- Filter-Chips im Katalog gehen von der Kategorie auf die KBs.
CREATE INDEX IF NOT EXISTS kb_category_links_category_idx ON kb_category_links (category_id);

-- +goose Down
DROP TABLE IF EXISTS kb_category_links;
DROP TABLE IF EXISTS kb_categories;
DROP TABLE IF EXISTS kb_subscriptions;

-- is_global zurueck in eine echte Spalte, Wert aus visibility rekonstruiert.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'knowledge_bases'
                 AND column_name = 'is_global'
                 AND is_generated = 'ALWAYS') THEN
        ALTER TABLE knowledge_bases DROP COLUMN is_global;
        ALTER TABLE knowledge_bases ADD COLUMN is_global boolean NOT NULL DEFAULT false;
        UPDATE knowledge_bases SET is_global = (visibility = 'public');
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE knowledge_bases DROP CONSTRAINT IF EXISTS knowledge_bases_visibility_check;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS auto_subscribe;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS visibility;
