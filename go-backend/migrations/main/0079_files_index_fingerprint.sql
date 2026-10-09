-- +goose Up
-- User file library, phase 2: hash of every overlay-resolved setting that
-- shaped this KB copy's stored index (processor.IndexFingerprint). Written
-- only on a 'completed' ingest or copy; NULL = unknown (pre-phase-2 rows,
-- partial/error ingests, non-library files) and never a copy donor.
ALTER TABLE files ADD COLUMN IF NOT EXISTS index_fingerprint text;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS index_fingerprint;
