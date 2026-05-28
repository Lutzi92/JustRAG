CREATE TABLE "confluence_connections" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"user_id" uuid NOT NULL,
	"token" text NOT NULL,
	"display_name" varchar(255),
	"status" varchar(50) DEFAULT 'active' NOT NULL,
	"error_message" text,
	"last_verified_at" timestamp,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "confluence_sources" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"connection_id" uuid NOT NULL,
	"space_key" varchar(255) NOT NULL,
	"root_page_id" varchar(255),
	"root_page_title" varchar(255),
	"include_attachments" boolean DEFAULT false NOT NULL,
	"sync_interval" integer,
	"status" varchar(50) DEFAULT 'active' NOT NULL,
	"error_message" text,
	"consecutive_failures" integer DEFAULT 0 NOT NULL,
	"last_synced_at" timestamp,
	"page_count" integer DEFAULT 0 NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "groups" DROP CONSTRAINT "groups_created_by_users_id_fk";
--> statement-breakpoint
ALTER TABLE "groups" ALTER COLUMN "created_by" DROP NOT NULL;--> statement-breakpoint
ALTER TABLE "files" ADD COLUMN "confluence_source_id" uuid;--> statement-breakpoint
ALTER TABLE "files" ADD COLUMN "confluence_page_id" varchar(255);--> statement-breakpoint
ALTER TABLE "confluence_connections" ADD CONSTRAINT "confluence_connections_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "confluence_sources" ADD CONSTRAINT "confluence_sources_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "confluence_sources" ADD CONSTRAINT "confluence_sources_connection_id_confluence_connections_id_fk" FOREIGN KEY ("connection_id") REFERENCES "public"."confluence_connections"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "confluence_connections_user_id_uniq" ON "confluence_connections" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "confluence_sources_kb_id_idx" ON "confluence_sources" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "confluence_sources_connection_id_idx" ON "confluence_sources" USING btree ("connection_id");--> statement-breakpoint
CREATE INDEX "confluence_sources_status_idx" ON "confluence_sources" USING btree ("status");--> statement-breakpoint
ALTER TABLE "files" ADD CONSTRAINT "files_confluence_source_id_confluence_sources_id_fk" FOREIGN KEY ("confluence_source_id") REFERENCES "public"."confluence_sources"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_users_id_fk" FOREIGN KEY ("created_by") REFERENCES "public"."users"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "files_confluence_source_id_idx" ON "files" USING btree ("confluence_source_id");