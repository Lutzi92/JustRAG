-- +goose Up
CREATE TABLE kb_site_configs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id      uuid NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    key        varchar(255) NOT NULL,
    value      text,
    updated_at timestamp NOT NULL DEFAULT now(),
    UNIQUE (kb_id, key)
);
CREATE INDEX kb_site_configs_kb_id_idx ON kb_site_configs (kb_id);

-- +goose Down
DROP TABLE IF EXISTS kb_site_configs;
