-- +goose Up
-- User file library, phase 3: KB-less library chats. kb_id NULL + type
-- 'library'. Every KB-scoped query filters kb_id = X and never sees these.
ALTER TABLE chats ALTER COLUMN kb_id DROP NOT NULL;
CREATE TABLE IF NOT EXISTS chat_file_refs (
    chat_id      uuid NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_file_id uuid NOT NULL REFERENCES user_files(id) ON DELETE CASCADE,
    added_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_file_id)
);
CREATE INDEX IF NOT EXISTS chat_file_refs_user_file_idx ON chat_file_refs (user_file_id);

-- +goose Down
DROP TABLE IF EXISTS chat_file_refs;
-- Rows with kb_id NULL must be removed before NOT NULL can return.
DELETE FROM chats WHERE kb_id IS NULL;
ALTER TABLE chats ALTER COLUMN kb_id SET NOT NULL;
