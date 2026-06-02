-- +goose Up
CREATE TABLE IF NOT EXISTS message_chunks (
  message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  chunk_id   uuid NOT NULL,
  kb_id      uuid NOT NULL,
  position   int  NOT NULL DEFAULT 0,
  PRIMARY KEY (message_id, chunk_id)
);
-- Read path: aggregate net signal for a candidate set of chunk_ids within a KB.
CREATE INDEX IF NOT EXISTS message_chunks_kb_chunk_idx ON message_chunks (kb_id, chunk_id);

-- +goose Down
DROP TABLE IF EXISTS message_chunks;
