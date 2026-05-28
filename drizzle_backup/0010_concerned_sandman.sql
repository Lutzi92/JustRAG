CREATE TABLE "ai_models" (
    "id" uuid PRIMARY KEY DEFAULT gen_random_uuid () NOT NULL,
    "provider_id" uuid NOT NULL,
    "name" varchar(255) NOT NULL,
    "is_reasoning" boolean DEFAULT false NOT NULL,
    "is_embedding" boolean DEFAULT false NOT NULL,
    "created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "ai_configs" RENAME TO "ai_providers";
--> statement-breakpoint
ALTER TABLE "knowledge_bases"
DROP CONSTRAINT "knowledge_bases_ai_config_id_ai_configs_id_fk";
--> statement-breakpoint
ALTER TABLE "ai_models"
ADD CONSTRAINT "ai_models_provider_id_ai_providers_id_fk" FOREIGN KEY ("provider_id") REFERENCES "public"."ai_providers" ("id") ON DELETE cascade ON UPDATE no action;
--> statement-breakpoint
ALTER TABLE "knowledge_bases"
ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_providers" ("id") ON DELETE no action ON UPDATE no action;
--> statement-breakpoint
ALTER TABLE "ai_providers" DROP COLUMN "chat_model";
--> statement-breakpoint
ALTER TABLE "ai_providers" DROP COLUMN "embedding_model";
--> statement-breakpoint
ALTER TABLE "ai_providers" DROP COLUMN "reasoning_models";