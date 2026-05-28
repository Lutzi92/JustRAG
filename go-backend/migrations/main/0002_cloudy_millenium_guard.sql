-- +goose Up
ALTER TABLE "ai_models" ADD COLUMN "is_rerank" boolean DEFAULT false NOT NULL;
ALTER TABLE "knowledge_bases" ADD COLUMN "rerank_model" varchar(255);

-- +goose Down
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "rerank_model";
ALTER TABLE "ai_models" DROP COLUMN IF EXISTS "is_rerank";
