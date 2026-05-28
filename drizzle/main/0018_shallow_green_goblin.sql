ALTER TABLE "background_jobs" DISABLE ROW LEVEL SECURITY;--> statement-breakpoint
DROP TABLE "background_jobs" CASCADE;--> statement-breakpoint
CREATE INDEX "ai_models_provider_id_idx" ON "ai_models" USING btree ("provider_id");--> statement-breakpoint
CREATE INDEX "files_status_idx" ON "files" USING btree ("status");--> statement-breakpoint
CREATE INDEX "files_type_idx" ON "files" USING btree ("type");--> statement-breakpoint
CREATE INDEX "messages_created_at_idx" ON "messages" USING btree ("created_at");