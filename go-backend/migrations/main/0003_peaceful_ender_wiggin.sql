-- +goose Up
ALTER TABLE "users" ADD COLUMN "email" varchar(255);

-- +goose Down
ALTER TABLE "users" DROP COLUMN IF EXISTS "email";
