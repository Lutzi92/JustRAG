-- +goose Up
CREATE TABLE "message_feedback_events" (
    "id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
    "message_id" uuid NOT NULL REFERENCES "messages"("id") ON DELETE CASCADE,
    "user_id" uuid REFERENCES "users"("id"),
    "rating" varchar(20) NOT NULL CHECK ("rating" IN ('positive', 'negative', 'cleared')),
    "comment" text,
    "created_at" timestamp NOT NULL DEFAULT now()
);

CREATE INDEX "message_feedback_events_message_id_created_at_idx"
    ON "message_feedback_events" ("message_id", "created_at" DESC);

ALTER TABLE "messages" ADD COLUMN "feedback_comment" text;
ALTER TABLE "messages" ADD COLUMN "feedback_updated_at" timestamp;

-- +goose Down
ALTER TABLE "messages" DROP COLUMN IF EXISTS "feedback_updated_at";
ALTER TABLE "messages" DROP COLUMN IF EXISTS "feedback_comment";
DROP TABLE IF EXISTS "message_feedback_events";
