-- +goose Up
INSERT INTO "site_configs" ("key", "value") VALUES ('contextual_enrichment', 'true') ON CONFLICT (key) DO NOTHING;
INSERT INTO "site_configs" ("key", "value") VALUES ('contextual_enrichment_model', '') ON CONFLICT (key) DO NOTHING;
INSERT INTO "site_configs" ("key", "value") VALUES ('factcheck_in_chat', 'true') ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM "site_configs" WHERE "key" = 'contextual_enrichment' AND "value" = 'true';
DELETE FROM "site_configs" WHERE "key" = 'contextual_enrichment_model' AND "value" = '';
DELETE FROM "site_configs" WHERE "key" = 'factcheck_in_chat' AND "value" = 'true';
