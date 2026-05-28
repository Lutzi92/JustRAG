ALTER TABLE "files" ADD COLUMN "origin" varchar(50) DEFAULT 'upload' NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "is_pro" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "ai_config_id" uuid;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "chat_model" varchar(255);--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "embedding_model" varchar(255);--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_configs_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_configs"("id") ON DELETE no action ON UPDATE no action;