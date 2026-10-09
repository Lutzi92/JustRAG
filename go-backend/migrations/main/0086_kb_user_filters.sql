-- +goose Up
-- Per-user topic filters for the shell's chip row ("Alle" / "Favoriten" /
-- the user's own categories). Written by internal/kbfilters and joined into
-- the session-facing KB reads (internal/kb's *WithUserFilters list queries and
-- GetKnowledgeBase, internal/kbsubs.Catalog). The API-key listings never read
-- them.
--
-- Deliberately separate from kb_categories / kb_category_links: those are a
-- single, system-admin-curated taxonomy that attaches to PUBLIC KBs and drives
-- the catalog's filter tabs. These tables carry the opposite visibility rule —
-- one owner per row, any topic that owner can see. A shared table would have to
-- mix both rules in one row.

-- Row existence IS the flag: there is no is_favorite = false, un-favoriting
-- deletes the row. Nothing else hangs off it, so no surrogate id.
CREATE TABLE IF NOT EXISTS kb_favorites (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kb_id      uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kb_id)
);

-- The PK leads on user_id and serves both "is this KB one of mine" and the
-- users cascade. Deleting a KB scans by kb_id, which the PK cannot serve.
CREATE INDEX IF NOT EXISTS kb_favorites_kb_idx ON kb_favorites (kb_id);

CREATE TABLE IF NOT EXISTS kb_user_categories (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       varchar(100) NOT NULL,
    sort_order int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive per user: two chips reading "Studium" and "studium" are
-- indistinguishable in the filter row, so the second one is a mistake, not a
-- second category. Global uniqueness would be wrong — two users naming their
-- own category "Studium" is the normal case.
CREATE UNIQUE INDEX IF NOT EXISTS kb_user_categories_user_name_idx
    ON kb_user_categories (user_id, LOWER(name));

-- The target of kb_user_category_links' composite FK below. (id, user_id) is
-- already unique because id alone is the PK; the constraint exists only so a
-- foreign key can reference the pair. Guarded by a catalog check rather than
-- DROP ... IF EXISTS / ADD, because on a re-run the dependent FK would make
-- the DROP fail.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'kb_user_categories_id_user_key'
          AND conrelid = 'kb_user_categories'::regclass
    ) THEN
        ALTER TABLE kb_user_categories
            ADD CONSTRAINT kb_user_categories_id_user_key UNIQUE (id, user_id);
    END IF;
END $$;
-- +goose StatementEnd

-- user_id is redundant next to category_id on purpose. Two reasons, and the
-- second is the load-bearing one:
--   1. "Which of MY categories does this topic carry" filters by user_id and
--      kb_id without joining kb_user_categories just to check ownership.
--   2. The composite FK (category_id, user_id) -> kb_user_categories (id,
--      user_id) makes "assigned somebody else's category" unrepresentable in
--      the database. A handler-side ownership guard would be the only defence
--      otherwise, and a handler-side guard is one forgotten call away from a
--      cross-user write.
CREATE TABLE IF NOT EXISTS kb_user_category_links (
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id uuid NOT NULL,
    kb_id       uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, category_id, kb_id),
    FOREIGN KEY (category_id, user_id)
        REFERENCES kb_user_categories (id, user_id) ON DELETE CASCADE
);

-- Two access paths the PK (user_id, category_id, kb_id) cannot serve on its
-- own: the knowledge_bases cascade, which scans by kb_id, and the per-row
-- lookup in the KB list queries, which is an equality on (kb_id, user_id).
-- Deleting a category scans (user_id, category_id) and uses the PK prefix.
CREATE INDEX IF NOT EXISTS kb_user_category_links_kb_user_idx
    ON kb_user_category_links (kb_id, user_id);

-- +goose Down
DROP TABLE IF EXISTS kb_user_category_links;
DROP TABLE IF EXISTS kb_user_categories;
DROP TABLE IF EXISTS kb_favorites;
