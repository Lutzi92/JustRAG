-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'chats' AND column_name = 'type'
    ) THEN
        ALTER TABLE "chats" ADD COLUMN "type" varchar(20) DEFAULT 'chat' NOT NULL;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE "chats" DROP COLUMN IF EXISTS "type";
