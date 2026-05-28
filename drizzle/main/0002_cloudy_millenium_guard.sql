ALTER TABLE "ai_models" ADD COLUMN "is_rerank" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "knowledge_bases" ADD COLUMN "rerank_model" varchar(255);