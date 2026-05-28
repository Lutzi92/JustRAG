CREATE TABLE "global_kb_editors" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "is_global" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "header_text" text;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "example_prompts" text;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "studio_config" jsonb;--> statement-breakpoint
ALTER TABLE "global_kb_editors" ADD CONSTRAINT "global_kb_editors_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "global_kb_editors" ADD CONSTRAINT "global_kb_editors_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "global_kb_editors_kb_id_idx" ON "global_kb_editors" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "global_kb_editors_user_id_idx" ON "global_kb_editors" USING btree ("user_id");