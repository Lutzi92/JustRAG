ALTER TABLE "ai_models" ADD COLUMN "is_stt" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "stt_model" varchar(255);