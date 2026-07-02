-- +goose Up
-- ai_models.dimensions was display-only metadata until now; the backend is
-- starting to send it as the OpenAI-style `dimensions` embedding-request
-- parameter (MRL truncation). Semantics change: 0 = "auto" (model-native
-- size, field omitted from requests), > 0 = requested truncated size.
--
-- Reset every existing row to 0: values stored so far (mostly the old 1536
-- column default) were never applied, and letting them activate on deploy
-- would silently truncate embeddings — queries would target a different
-- dim-keyed vector table and the entire corpus would become invisible.
-- Operators opt in per model by setting a value in the admin UI afterwards.
ALTER TABLE "ai_models" ALTER COLUMN "dimensions" SET DEFAULT 0;
UPDATE "ai_models" SET "dimensions" = 0;

-- +goose Down
-- Restore the historical default. Row values are not restored (they were
-- inert metadata); re-seed manually if needed.
ALTER TABLE "ai_models" ALTER COLUMN "dimensions" SET DEFAULT 1536;
