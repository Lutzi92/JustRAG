-- +goose Up
ALTER TABLE "ai_models" ADD COLUMN "dimensions" integer DEFAULT 1536 NOT NULL;

-- +goose Down
ALTER TABLE "ai_models" DROP COLUMN IF EXISTS "dimensions";
