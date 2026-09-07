-- +goose Up
-- Trust surfaces (RAG SOTA review 2026-09-05, Wave 5 — W5-R6/R8, R11).
--
-- One migration for the whole wave. The ragas_samples table is consumed by
-- Task 1 (the sampler's writer + the nightly aggregate); the messages/files
-- columns below are consumed by later tasks in the same wave and ship here so
-- the wave carries a single schema change rather than three.
--
-- No backfill anywhere: there is nothing to derive the new values from
-- (judge scores, conflict reports and screening verdicts are all produced at
-- answer/ingest time), and a wrong-by-construction backfill would be worse
-- than a NULL.

-- ragas_samples records one row per RAGAS production sample. Until now the
-- worker (internal/worker/ragas_sample.go) emitted Prometheus histograms and
-- nothing else, so a score could be alerted on but never attributed: no way
-- to ask "which KB regressed", "which message scored 0.2", or "was the judge
-- itself broken that night". The histograms stay — they remain the alerting
-- surface — and this table is the attribution surface behind them.
--
-- Every score is nullable and stays NULL when that judge prompt failed:
-- writing 0.0 for a failed prompt would poison every mean with false
-- negatives, which is exactly the mistake RecordRAGASSample already avoids
-- for the histograms. judge_errors keeps the reason, so a row with three
-- NULLs is readable as "the judge broke", not "the answer was terrible".
--
-- coverage is the fourth (W4-R5) judge metric. It stays NULL on this path —
-- coverage needs a question's expected points, which a sampled production
-- turn does not have — and the column exists so a future golden-point source
-- does not need another migration.
--
-- Both FKs are ON DELETE SET NULL, deliberately: deleting a KB or a chat
-- message must not silently shrink the historical quality record. A row whose
-- kb_id has gone NULL still counts toward the table's size (and is still
-- pruned by retention) but is skipped by the per-KB aggregate.
CREATE TABLE IF NOT EXISTS ragas_samples (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id        UUID REFERENCES messages(id) ON DELETE SET NULL,
    kb_id             UUID REFERENCES knowledge_bases(id) ON DELETE SET NULL,
    faithfulness      REAL,
    answer_relevance  REAL,
    context_precision REAL,
    coverage          REAL,
    judge_model       TEXT NOT NULL DEFAULT '',
    judge_errors      JSONB,
    sampled_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The only read pattern is "this KB's samples in the last 24 h" (the nightly
-- aggregate and the admin KB-overview column), so (kb_id, sampled_at DESC) is
-- the covering order. Plain CREATE INDEX rather than CONCURRENTLY: the table
-- is created empty in this same migration, so nothing can be writing to it
-- and the SHARE lock is free (migrations/README.md's large-table rule applies
-- to pre-existing tables only).
CREATE INDEX IF NOT EXISTS ragas_samples_kb_sampled_idx
    ON ragas_samples (kb_id, sampled_at DESC);

-- messages.conflicts holds the conflict report (W5 conflict surfacing): the
-- contradictions found among the chunks an answer was built from, persisted
-- alongside the answer so reopening a chat shows the same badge the live SSE
-- frame did. NULL = the pass did not run (flag off, or fewer than two
-- distinct source files).
ALTER TABLE messages ADD COLUMN IF NOT EXISTS conflicts JSONB;

-- files.injection_flag / injection_detail record the ingest-time prompt-
-- injection screening verdict. The flag is NOT NULL DEFAULT false so the file
-- list can filter on it without a COALESCE; false therefore means BOTH "clean"
-- and "ingested before screening existed" — the detail column (NULL when the
-- screen never ran) is what distinguishes them. Adding a column with a
-- non-volatile default is metadata-only on PG 11+, so this does not rewrite
-- the (large) files table.
ALTER TABLE files ADD COLUMN IF NOT EXISTS injection_flag BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE files ADD COLUMN IF NOT EXISTS injection_detail JSONB;

-- +goose Down
ALTER TABLE files DROP COLUMN IF EXISTS injection_detail;
ALTER TABLE files DROP COLUMN IF EXISTS injection_flag;
ALTER TABLE messages DROP COLUMN IF EXISTS conflicts;
DROP INDEX IF EXISTS ragas_samples_kb_sampled_idx;
DROP TABLE IF EXISTS ragas_samples;
