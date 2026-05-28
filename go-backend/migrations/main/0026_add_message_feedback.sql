-- +goose Up
ALTER TABLE "messages" ADD COLUMN "feedback" varchar(20);

-- +goose Down
ALTER TABLE "messages" DROP COLUMN IF EXISTS "feedback";
