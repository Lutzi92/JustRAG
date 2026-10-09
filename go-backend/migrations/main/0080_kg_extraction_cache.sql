-- +goose Up
-- User file library, phase 2: per-library-file cache of KG extractions so a
-- copied index can rebuild its KB's graph without LLM calls. Pure derived
-- data (nothing outside FK reach) → ON DELETE CASCADE is safe here.
CREATE TABLE IF NOT EXISTS kg_extraction_cache (
    user_file_id   uuid NOT NULL REFERENCES user_files(id) ON DELETE CASCADE,
    content_hash   text NOT NULL,
    model          text NOT NULL,
    prompt_version int  NOT NULL,
    lang           text NOT NULL,
    extraction     jsonb NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_file_id, content_hash, model, prompt_version, lang)
);

-- +goose Down
DROP TABLE IF EXISTS kg_extraction_cache;
