-- +goose Up
ALTER TABLE "users" ADD COLUMN "auth_method" varchar(20) DEFAULT 'local' NOT NULL;

-- +goose Down
ALTER TABLE "users" DROP COLUMN IF EXISTS "auth_method";
