-- +goose Up
CREATE TABLE "api_keys" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"user_id" uuid NOT NULL,
	"name" varchar(100) NOT NULL,
	"key_hash" text NOT NULL,
	"key_prefix" varchar(13) NOT NULL,
	"last_used_at" timestamp,
	"expires_at" timestamp,
	"created_at" timestamp DEFAULT now() NOT NULL
);

ALTER TABLE "api_keys" ADD CONSTRAINT "api_keys_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
CREATE INDEX "api_keys_user_id_idx" ON "api_keys" USING btree ("user_id");
CREATE INDEX "api_keys_key_prefix_idx" ON "api_keys" USING btree ("key_prefix");

-- +goose Down
DROP TABLE IF EXISTS "api_keys" CASCADE;
