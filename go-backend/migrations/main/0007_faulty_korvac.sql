-- +goose Up
ALTER TABLE "ai_models" ADD COLUMN "is_stt" boolean DEFAULT false NOT NULL;
ALTER TABLE "knowledge_bases" ADD COLUMN "stt_model" varchar(255);

-- +goose Down
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "stt_model";
ALTER TABLE "ai_models" DROP COLUMN IF EXISTS "is_stt";
