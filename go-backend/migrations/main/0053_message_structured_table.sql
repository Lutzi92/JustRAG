-- +goose Up
ALTER TABLE "messages" ADD COLUMN "structured_table" jsonb;

-- +goose Down
ALTER TABLE "messages" DROP COLUMN IF EXISTS "structured_table";
