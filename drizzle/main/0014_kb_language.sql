DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'knowledge_bases' AND column_name = 'language'
    ) THEN
        ALTER TABLE "knowledge_bases" ADD COLUMN "language" varchar(5) DEFAULT 'de' NOT NULL;
    END IF;
END $$;
