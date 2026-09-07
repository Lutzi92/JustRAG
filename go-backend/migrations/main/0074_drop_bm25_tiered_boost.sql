-- +goose Up
-- Wave 7 (W7-R2): bm25_tiered_boost_enabled is gone from the code (the key,
-- its per-KB registry row, the keyword-arm CASE and the --bm25-tiered-boost
-- eval override). Delete any stored value so the admin config surface stops
-- listing an orphaned row nothing reads. Idempotent: a deployment that never
-- set the key deletes zero rows.
DELETE FROM site_configs WHERE key = 'bm25_tiered_boost_enabled';
DELETE FROM kb_site_configs WHERE key = 'bm25_tiered_boost_enabled';

-- +goose Down
-- Deliberately a no-op: the deleted rows configured a deprecated feature whose
-- code no longer exists, so restoring them would recreate an orphaned row that
-- nothing reads. Rolling back to a build that still has the boost means
-- re-setting the key by hand.
SELECT 1;
