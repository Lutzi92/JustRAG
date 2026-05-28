CREATE TABLE "rss_feeds" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"url" text NOT NULL,
	"title" varchar(255),
	"poll_interval" integer DEFAULT 60 NOT NULL,
	"status" varchar(50) DEFAULT 'active' NOT NULL,
	"error_message" text,
	"consecutive_failures" integer DEFAULT 0 NOT NULL,
	"last_polled_at" timestamp,
	"item_count" integer DEFAULT 0 NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "files" ADD COLUMN "rss_feed_id" uuid;--> statement-breakpoint
ALTER TABLE "rss_feeds" ADD CONSTRAINT "rss_feeds_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "rss_feeds_kb_id_idx" ON "rss_feeds" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "rss_feeds_status_idx" ON "rss_feeds" USING btree ("status");--> statement-breakpoint
ALTER TABLE "files" ADD CONSTRAINT "files_rss_feed_id_rss_feeds_id_fk" FOREIGN KEY ("rss_feed_id") REFERENCES "public"."rss_feeds"("id") ON DELETE cascade ON UPDATE no action;