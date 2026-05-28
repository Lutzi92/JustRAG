-- +goose Up
ALTER TABLE "groups" DROP CONSTRAINT "groups_created_by_users_id_fk";

ALTER TABLE "knowledge_bases" DROP CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk";

ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_users_id_fk" FOREIGN KEY ("created_by") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_providers"("id") ON DELETE set null ON UPDATE no action;
ALTER TABLE "messages" ADD CONSTRAINT "messages_parent_message_id_messages_id_fk" FOREIGN KEY ("parent_message_id") REFERENCES "public"."messages"("id") ON DELETE set null ON UPDATE no action;
CREATE UNIQUE INDEX "global_kb_editors_kb_user_uniq" ON "global_kb_editors" USING btree ("kb_id","user_id");

-- +goose Down
DROP INDEX IF EXISTS "global_kb_editors_kb_user_uniq";
ALTER TABLE "messages" DROP CONSTRAINT IF EXISTS "messages_parent_message_id_messages_id_fk";
ALTER TABLE "knowledge_bases" DROP CONSTRAINT IF EXISTS "knowledge_bases_ai_config_id_ai_providers_id_fk";
ALTER TABLE "groups" DROP CONSTRAINT IF EXISTS "groups_created_by_users_id_fk";
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_providers"("id") ON DELETE no action ON UPDATE no action;
ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_users_id_fk" FOREIGN KEY ("created_by") REFERENCES "public"."users"("id") ON DELETE no action ON UPDATE no action;
