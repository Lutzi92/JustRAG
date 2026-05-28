-- +goose Up
CREATE TABLE "global_kb_editors" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

ALTER TABLE "knowledge_bases" ADD COLUMN "is_global" boolean DEFAULT false NOT NULL;
ALTER TABLE "knowledge_bases" ADD COLUMN "header_text" text;
ALTER TABLE "knowledge_bases" ADD COLUMN "example_prompts" text;
ALTER TABLE "knowledge_bases" ADD COLUMN "studio_config" jsonb;
ALTER TABLE "global_kb_editors" ADD CONSTRAINT "global_kb_editors_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "global_kb_editors" ADD CONSTRAINT "global_kb_editors_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
CREATE INDEX "global_kb_editors_kb_id_idx" ON "global_kb_editors" USING btree ("kb_id");
CREATE INDEX "global_kb_editors_user_id_idx" ON "global_kb_editors" USING btree ("user_id");

-- +goose Down
DROP TABLE IF EXISTS "global_kb_editors" CASCADE;
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "studio_config";
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "example_prompts";
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "header_text";
ALTER TABLE "knowledge_bases" DROP COLUMN IF EXISTS "is_global";
