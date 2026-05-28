ALTER TABLE "groups" DROP CONSTRAINT "groups_created_by_users_id_fk";
--> statement-breakpoint
ALTER TABLE "knowledge_bases" DROP CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk";
--> statement-breakpoint
ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_users_id_fk" FOREIGN KEY ("created_by") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_providers"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "messages" ADD CONSTRAINT "messages_parent_message_id_messages_id_fk" FOREIGN KEY ("parent_message_id") REFERENCES "public"."messages"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "global_kb_editors_kb_user_uniq" ON "global_kb_editors" USING btree ("kb_id","user_id");