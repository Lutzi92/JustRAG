-- +goose Up
CREATE INDEX "idx_kb_ai_config" ON "knowledge_bases" USING btree ("ai_config_id");
CREATE INDEX "idx_jobs_status_type_created" ON "background_jobs" USING btree ("status","type","created_at");

-- +goose Down
DROP INDEX IF EXISTS "idx_jobs_status_type_created";
DROP INDEX IF EXISTS "idx_kb_ai_config";
