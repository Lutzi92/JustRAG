package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsureGoldenSetKBID backfills eval_golden_sets.kb_id and, once every row has a
// kb_id, enforces NOT NULL + FK. Idempotent: once the column is NOT NULL it
// returns immediately.
//
// Backfill order:
//  1. Rows whose description carries the generate-from-corpus provenance
//     "kb=<uuid>" get that kb_id.
//  2. Remaining orphans (manual uploads, no provenance) are assigned to
//     EVAL_ORPHAN_FALLBACK_KB_ID when it names an existing KB.
//
// Non-blocking by design: if any orphan remains afterwards (no fallback set, or
// the fallback is invalid), this does NOT fail — it logs a loud WARN naming the
// orphans and leaves kb_id nullable so app startup is never bricked by a
// one-time data concern. Enforcement runs automatically on a later migrate once
// the orphans are assigned or deleted. New uploads always set kb_id, so only
// legacy rows are ever affected, and they stay invisible to KB-scoped queries
// (which filter on kb_id) until resolved.
//
// Decision: golden sets are strictly per-KB (kb_id NOT NULL), see
// docs/superpowers/specs/2026-06-02-per-kb-config-and-eval-design.md §Golden-set.
func EnsureGoldenSetKBID(ctx context.Context, pool *pgxpool.Pool) error {
	// 0. Idempotency: bail if kb_id is already NOT NULL.
	var isNullable bool
	const nullableQ = `
		SELECT is_nullable = 'YES'
		FROM information_schema.columns
		WHERE table_name = 'eval_golden_sets' AND column_name = 'kb_id'`
	if err := pool.QueryRow(ctx, nullableQ).Scan(&isNullable); err != nil {
		return fmt.Errorf("ensure golden_set kb_id: introspect column: %w", err)
	}
	if !isNullable {
		return nil // already enforced
	}

	// 1. Provenance backfill: "kb=<uuid>" in the description.
	const provenanceQ = `
		UPDATE eval_golden_sets
		SET kb_id = (substring(description from 'kb=([0-9a-fA-F-]{36})'))::uuid
		WHERE kb_id IS NULL
		  AND description ~ 'kb=[0-9a-fA-F-]{36}'`
	if _, err := pool.Exec(ctx, provenanceQ); err != nil {
		return fmt.Errorf("ensure golden_set kb_id: provenance backfill: %w", err)
	}

	// 2. Assign remaining orphans (no recoverable provenance) to the configured
	// fallback KB, when one is set and names an existing KB. An unset or invalid
	// fallback is NOT fatal here — step 3 decides whether to enforce.
	if fallback := strings.TrimSpace(os.Getenv("EVAL_ORPHAN_FALLBACK_KB_ID")); fallback != "" {
		fbID, perr := uuid.Parse(fallback)
		if perr != nil {
			slog.Warn("ensure golden_set kb_id: EVAL_ORPHAN_FALLBACK_KB_ID is not a valid UUID; orphans left unassigned", "value", fallback)
		} else {
			var kbExists bool
			if err := pool.QueryRow(ctx, `SELECT exists(SELECT 1 FROM knowledge_bases WHERE id = $1)`, fbID).Scan(&kbExists); err != nil {
				return fmt.Errorf("ensure golden_set kb_id: check fallback kb: %w", err)
			}
			if !kbExists {
				slog.Warn("ensure golden_set kb_id: EVAL_ORPHAN_FALLBACK_KB_ID does not reference an existing KB; orphans left unassigned", "kb_id", fbID.String())
			} else if _, err := pool.Exec(ctx, `UPDATE eval_golden_sets SET kb_id = $1 WHERE kb_id IS NULL`, fbID); err != nil {
				return fmt.Errorf("ensure golden_set kb_id: assign orphans: %w", err)
			}
		}
	}

	// 3. If any orphans remain, leave kb_id nullable and warn loudly rather than
	// hard-failing startup. Enforcement happens on a future migrate once they're
	// resolved.
	orphans, err := listOrphanGoldenSets(ctx, pool)
	if err != nil {
		return fmt.Errorf("ensure golden_set kb_id: list orphans: %w", err)
	}
	if len(orphans) > 0 {
		slog.Warn("ensure golden_set kb_id: leaving kb_id nullable — golden sets without a recoverable KB remain; set EVAL_ORPHAN_FALLBACK_KB_ID to an existing KB id (or delete them) and re-run migrate to enforce NOT NULL",
			"orphan_count", len(orphans),
			"orphans", strings.Join(orphans, "; "),
		)
		return nil
	}

	// 4. Enforce NOT NULL + FK + index atomically so a partial failure cannot
	// leave the column NOT NULL while the FK/index are absent (which the
	// idempotency check would then skip forever).
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ensure golden_set kb_id: begin enforce tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`ALTER TABLE eval_golden_sets ALTER COLUMN kb_id SET NOT NULL`,
		`ALTER TABLE eval_golden_sets ADD CONSTRAINT eval_golden_sets_kb_id_fkey FOREIGN KEY (kb_id) REFERENCES knowledge_bases(id) ON DELETE CASCADE`,
		`CREATE INDEX IF NOT EXISTS eval_golden_sets_kb_id_idx ON eval_golden_sets (kb_id)`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure golden_set kb_id: enforce constraints: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ensure golden_set kb_id: commit enforce tx: %w", err)
	}
	return nil
}

// listOrphanGoldenSets returns "id (name)" strings for golden sets whose kb_id
// is still NULL, for the operator-facing WARN that names exactly which fixtures
// need a KB before NOT NULL can be enforced.
func listOrphanGoldenSets(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id, coalesce(name, '') FROM eval_golden_sets WHERE kb_id IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s (%s)", id, name))
	}
	return out, rows.Err()
}
