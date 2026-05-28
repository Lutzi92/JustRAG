-- +goose Up
ALTER TABLE "background_jobs" DISABLE ROW LEVEL SECURITY;
DROP TABLE "background_jobs" CASCADE;
CREATE INDEX "ai_models_provider_id_idx" ON "ai_models" USING btree ("provider_id");
CREATE INDEX "files_status_idx" ON "files" USING btree ("status");
CREATE INDEX "files_type_idx" ON "files" USING btree ("type");
CREATE INDEX "messages_created_at_idx" ON "messages" USING btree ("created_at");

-- +goose Down
DROP INDEX IF EXISTS "messages_created_at_idx";
DROP INDEX IF EXISTS "files_type_idx";
DROP INDEX IF EXISTS "files_status_idx";
DROP INDEX IF EXISTS "ai_models_provider_id_idx";
-- Recreate background_jobs as it was after 0008 so a 0018-only revert leaves
-- the table behind for the prior-migration state.
CREATE TABLE IF NOT EXISTS "background_jobs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"type" text NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"data" jsonb DEFAULT '{}'::jsonb,
	"result" jsonb,
	"error" text,
	"attempts" integer DEFAULT 0 NOT NULL,
	"max_attempts" integer DEFAULT 3 NOT NULL,
	"worker_id" text,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL,
	"processed_at" timestamp,
	"completed_at" timestamp
);
