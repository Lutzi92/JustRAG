-- +goose Up
CREATE TABLE "group_kbs" (
	"group_id" uuid NOT NULL,
	"kb_id" uuid NOT NULL,
	"added_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "group_kbs_group_id_kb_id_pk" PRIMARY KEY("group_id","kb_id")
);

CREATE TABLE "groups" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"name" varchar(255) NOT NULL,
	"description" text,
	"type" varchar(50) DEFAULT 'research_group' NOT NULL,
	"metadata" jsonb,
	"created_by" uuid NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);

CREATE TABLE "user_groups" (
	"group_id" uuid NOT NULL,
	"user_id" uuid NOT NULL,
	"role" varchar(50) DEFAULT 'member' NOT NULL,
	"joined_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "user_groups_group_id_user_id_pk" PRIMARY KEY("group_id","user_id")
);

ALTER TABLE "group_kbs" ADD CONSTRAINT "group_kbs_group_id_groups_id_fk" FOREIGN KEY ("group_id") REFERENCES "public"."groups"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "group_kbs" ADD CONSTRAINT "group_kbs_kb_id_knowledge_bases_id_fk" FOREIGN KEY ("kb_id") REFERENCES "public"."knowledge_bases"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "groups" ADD CONSTRAINT "groups_created_by_users_id_fk" FOREIGN KEY ("created_by") REFERENCES "public"."users"("id") ON DELETE no action ON UPDATE no action;
ALTER TABLE "user_groups" ADD CONSTRAINT "user_groups_group_id_groups_id_fk" FOREIGN KEY ("group_id") REFERENCES "public"."groups"("id") ON DELETE cascade ON UPDATE no action;
ALTER TABLE "user_groups" ADD CONSTRAINT "user_groups_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users"("id") ON DELETE cascade ON UPDATE no action;
CREATE INDEX "group_kbs_kb_id_idx" ON "group_kbs" USING btree ("kb_id");
CREATE INDEX "groups_created_by_idx" ON "groups" USING btree ("created_by");
CREATE INDEX "user_groups_user_id_idx" ON "user_groups" USING btree ("user_id");

-- +goose Down
DROP TABLE IF EXISTS "user_groups" CASCADE;
DROP TABLE IF EXISTS "group_kbs" CASCADE;
DROP TABLE IF EXISTS "groups" CASCADE;
