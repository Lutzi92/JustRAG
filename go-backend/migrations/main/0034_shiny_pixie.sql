-- +goose Up
ALTER TABLE "knowledge_bases" ADD COLUMN "system_prompt" text;

-- +goose Down
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "system_prompt";
