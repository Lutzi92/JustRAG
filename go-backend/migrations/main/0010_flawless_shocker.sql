-- +goose Up
CREATE TABLE "system_metrics" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"metric_name" varchar(100) NOT NULL,
	"metric_value" numeric NOT NULL,
	"metadata" jsonb,
	"recorded_at" timestamp DEFAULT now() NOT NULL
);

ALTER TABLE "users" ADD COLUMN "last_seen_at" timestamp;
CREATE INDEX "idx_system_metrics_name_time" ON "system_metrics" USING btree ("metric_name","recorded_at");

-- +goose Down
DROP INDEX IF EXISTS "idx_system_metrics_name_time";
ALTER TABLE "users" DROP COLUMN IF EXISTS "last_seen_at";
DROP TABLE IF EXISTS "system_metrics" CASCADE;
