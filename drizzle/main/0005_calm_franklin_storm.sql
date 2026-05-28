ALTER TABLE "ai_models" ADD COLUMN "is_tts" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "tts_model" varchar(255);