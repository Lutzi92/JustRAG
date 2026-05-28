-- +goose Up
ALTER TABLE "messages" ADD COLUMN "verification" jsonb;

-- +goose Down
ALTER TABLE "messages" DROP COLUMN "verification";
