-- +goose NO TRANSACTION
-- +goose Up
-- Full-text index on message content for GET /api/search's messages group
-- (internal/globalsearch/fulltext.go). messages had no text index at all,
-- only chat_id / created_at / parent_message_id btrees.
--
-- An expression index, not a GENERATED ... STORED tsvector column: adding a
-- stored column would rewrite and lock the whole messages table. The query
-- in internal/globalsearch repeats this expression exactly (messageTSVector);
-- change one and the planner silently stops using the index.
--
--   - 'simple' config, which only lowercases: the corpus is mixed German and
--     English with many code terms, and German stemming would mangle the
--     English words and the code terms. Switching needs a new index and the
--     matching query change (see ftsConfig in fulltext.go).
--   - left(content, 100000): a tsvector is capped at 1 MB and to_tsvector
--     raises an error beyond it. In an index expression that error would
--     abort the INSERT of an oversize message (breaking the chat write path)
--     and fail this build on any existing oversize row. 100 000 characters
--     stay well below the cap even for pathological input; see
--     maxIndexedChars in internal/globalsearch/fulltext.go for the measurement.
--
-- NO TRANSACTION + CONCURRENTLY because messages is a large table
-- (migrations/README.md): a plain CREATE INDEX would block chat writes for
-- the whole build. If the build fails it leaves an INVALID index that
-- IF NOT EXISTS then skips — drop it (DROP INDEX CONCURRENTLY
-- messages_content_fts_idx) and re-run. cmd/migrate connects with
-- statement_timeout=0, so the build is not cancelled half way. As for 0084,
-- run a single cmd/migrate instance: a concurrent one waiting for the
-- migration advisory lock holds a snapshot this build waits for, and is
-- aborted as a deadlock.

CREATE INDEX CONCURRENTLY IF NOT EXISTS messages_content_fts_idx
    ON messages USING gin (to_tsvector('simple', left(content, 100000)));

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS messages_content_fts_idx;
