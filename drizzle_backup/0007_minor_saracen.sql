CREATE INDEX "chats_kb_id_idx" ON "chats" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "chats_user_id_idx" ON "chats" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "document_chunks_file_id_idx" ON "document_chunks" USING btree ("file_id");--> statement-breakpoint
CREATE INDEX "files_kb_id_idx" ON "files" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "knowledge_base_shares_kb_id_idx" ON "knowledge_base_shares" USING btree ("kb_id");--> statement-breakpoint
CREATE INDEX "knowledge_base_shares_user_id_idx" ON "knowledge_base_shares" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "knowledge_bases_user_id_idx" ON "knowledge_bases" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "messages_chat_id_idx" ON "messages" USING btree ("chat_id");