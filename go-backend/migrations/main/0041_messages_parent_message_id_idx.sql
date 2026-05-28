-- +goose Up
-- Speeds up the recursive CTE in chat.GetMessageAncestors which walks the
-- parent_message_id chain upward. Partial index because most messages have a
-- NULL parent (the conversation root) and we only ever JOIN on non-NULL.
CREATE INDEX IF NOT EXISTS "messages_parent_message_id_idx"
    ON "messages" ("parent_message_id")
    WHERE "parent_message_id" IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS "messages_parent_message_id_idx";
