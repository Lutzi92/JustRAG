-- +goose Up
ALTER TABLE "messages" ADD COLUMN "trace_id" text;

-- +goose Down
ALTER TABLE "messages" DROP COLUMN "trace_id";
