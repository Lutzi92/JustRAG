-- +goose Up
ALTER TABLE "files" ALTER COLUMN "type" SET DATA TYPE varchar(255);

-- +goose Down
-- Best-effort revert: fails at runtime if existing data exceeds varchar(50).
ALTER TABLE "files" ALTER COLUMN "type" SET DATA TYPE varchar(50);
