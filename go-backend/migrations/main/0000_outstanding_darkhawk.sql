-- +goose Up
CREATE TABLE "ai_models" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"provider_id" uuid NOT NULL,
	"name" varchar(255) NOT NULL,
	"is_reasoning" boolean DEFAULT false NOT NULL,
	"is_embedding" boolean DEFAULT false NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "ai_providers" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"name" varchar(255) NOT NULL,
	"provider" varchar(255) NOT NULL,
	"api_key" text NOT NULL,
	"base_url" text,
	"is_active" boolean DEFAULT false NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "auth_providers" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"type" varchar(50) NOT NULL,
	"name" varchar(255) NOT NULL,
	"config" jsonb NOT NULL,
	"is_active" boolean DEFAULT true NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "chats" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"title" varchar(255) DEFAULT 'New Chat' NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "files" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"name" varchar(255) NOT NULL,
	"type" varchar(50) NOT NULL,
	"size" integer,
	"status" varchar(50) DEFAULT 'pending' NOT NULL,
	"progress" integer DEFAULT 0 NOT NULL,
	"origin" varchar(50) DEFAULT 'upload' NOT NULL,
	"storage_path" text,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "knowledge_base_shares" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"kb_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"permission" varchar(50) DEFAULT 'view' NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "knowledge_bases" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"name" varchar(255) NOT NULL,
	"user_id" uuid,
	"description" text,
	"is_pro" boolean DEFAULT false NOT NULL,
	"ai_config_id" uuid,
	"chat_model" varchar(255),
	"embedding_model" varchar(255),
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "messages" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"chat_id" uuid NOT NULL,
	"role" varchar(20) NOT NULL,
	"content" text NOT NULL,
	"sources" jsonb,
	"is_enhanced" boolean DEFAULT false NOT NULL,
	"enhanced_query" text,
	"reasoning" text,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "site_configs" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"key" varchar(255) NOT NULL,
	"value" text,
	"updated_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "site_configs_key_unique" UNIQUE("key")
);

CREATE TABLE "users" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"username" varchar(255) NOT NULL,
	"password_hash" text NOT NULL,
	"first_name" varchar(255),
	"last_name" varchar(255),
	"role" varchar(50) DEFAULT 'user' NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "users_username_unique" UNIQUE("username")
);

ALTER TABLE "ai_models" ADD CONSTRAINT "ai_models_provider_id_ai_providers_id_fk" FOREIGN KEY ("provider_id") REFERENCES "public"."ai_providers"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "chats" ADD CONSTRAINT "chats_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "chats" ADD CONSTRAINT "chats_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "files" ADD CONSTRAINT "files_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "knowledge_base_shares" ADD CONSTRAINT "knowledge_base_shares_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "knowledge_base_shares" ADD CONSTRAINT "knowledge_base_shares_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "knowledge_bases" ADD CONSTRAINT "knowledge_bases_ai_config_id_ai_providers_id_fk" FOREIGN KEY ("ai_config_id") REFERENCES "public"."ai_providers"("id") ON DELETE no action ON UPDATE no action;
ALTER TABLE "messages" ADD CONSTRAINT "messages_chat_id_chats_id_fk" FOREIGN KEY ("chat_id") REFERENCES "public"."chats"("id") ON DELETE cascade ON UPDATE no action;
CREATE INDEX "chats_kb_id_idx" ON "chats" USING btree ("kb_id");
CREATE INDEX "chats_user_id_idx" ON "chats" USING btree ("user_id");
CREATE INDEX "files_kb_id_idx" ON "files" USING btree ("kb_id");
CREATE INDEX "knowledge_base_shares_kb_id_idx" ON "knowledge_base_shares" USING btree ("kb_id");
CREATE INDEX "knowledge_base_shares_user_id_idx" ON "knowledge_base_shares" USING btree ("user_id");
CREATE INDEX "knowledge_bases_user_id_idx" ON "knowledge_bases" USING btree ("user_id");
CREATE INDEX "messages_chat_id_idx" ON "messages" USING btree ("chat_id");

-- +goose Down
DROP TABLE IF EXISTS "messages" CASCADE;
DROP TABLE IF EXISTS "knowledge_base_shares" CASCADE;
DROP TABLE IF EXISTS "knowledge_bases" CASCADE;
DROP TABLE IF EXISTS "files" CASCADE;
DROP TABLE IF EXISTS "chats" CASCADE;
DROP TABLE IF EXISTS "auth_providers" CASCADE;
DROP TABLE IF EXISTS "ai_models" CASCADE;
DROP TABLE IF EXISTS "ai_providers" CASCADE;
DROP TABLE IF EXISTS "site_configs" CASCADE;
DROP TABLE IF EXISTS "users" CASCADE;
