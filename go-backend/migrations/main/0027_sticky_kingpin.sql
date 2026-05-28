-- +goose Up
CREATE UNIQUE INDEX "knowledge_base_shares_kb_user_uniq" ON "knowledge_base_shares" USING btree ("kb_id","user_id");

-- +goose Down
DROP INDEX IF EXISTS "knowledge_base_shares_kb_user_uniq";
