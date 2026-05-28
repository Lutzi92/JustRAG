ALTER TABLE "messages" ADD COLUMN "parent_message_id" uuid;--> statement-breakpoint
CREATE INDEX IF NOT EXISTS "messages_parent_message_id_idx" ON "messages" USING btree ("parent_message_id");--> statement-breakpoint

-- Backfill existing messages: chain them linearly within each chat by created_at order
WITH ordered AS (
  SELECT id, chat_id,
         LAG(id) OVER (PARTITION BY chat_id ORDER BY created_at) AS prev_id
  FROM messages
)
UPDATE messages m
SET parent_message_id = o.prev_id
FROM ordered o
WHERE m.id = o.id AND o.prev_id IS NOT NULL;
