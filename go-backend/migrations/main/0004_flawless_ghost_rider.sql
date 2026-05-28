-- +goose Up
CREATE TABLE "generated_content" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"type" varchar(50) NOT NULL,
	"title" varchar(255) NOT NULL,
	"content" jsonb NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

ALTER TABLE "generated_content" ADD CONSTRAINT "generated_content_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "generated_content" ADD CONSTRAINT "generated_content_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
CREATE INDEX "generated_content_kb_id_idx" ON "generated_content" USING btree ("kb_id");
CREATE INDEX "generated_content_user_id_idx" ON "generated_content" USING btree ("user_id");

-- +goose Down
DROP TABLE IF EXISTS "generated_content" CASCADE;
