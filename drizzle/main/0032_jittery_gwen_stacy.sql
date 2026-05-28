ALTER TABLE "confluence_sources" ADD COLUMN "sync_progress" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "confluence_sources" ADD COLUMN "sync_total" integer DEFAULT 0 NOT NULL;