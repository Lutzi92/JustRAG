package ragassamples

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres-backed Store over the main pool.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main pool. A nil pool yields a store
// whose methods are no-ops, so a worker started without a main pool degrades
// to the pre-0072 Prometheus-only behaviour instead of panicking.
func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// nullableID maps an unset or empty id to a SQL NULL. Both cases occur: a
// sampled turn on a surface that persists no message carries an empty
// MessageID, and the ::uuid cast would reject "" rather than treat it as
// absent.
func nullableID(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

const insertSQL = `
	INSERT INTO ragas_samples
	    (message_id, kb_id, faithfulness, answer_relevance, context_precision,
	     coverage, judge_model, judge_errors, sampled_at)
	VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::jsonb, COALESCE($9, NOW()))`

// Insert writes one judged sample. The nil score pointers land as SQL NULL
// (see the package doc: a failed judge prompt must not be stored as 0.0), and
// an empty JudgeErrors slice lands as NULL rather than as `[]` so "no errors"
// and "errors not recorded" are not conflated in the JSONB.
func (s *PGStore) Insert(ctx context.Context, sample Sample) error {
	if s == nil || s.pool == nil {
		return nil
	}

	var errsJSON any
	if len(sample.JudgeErrors) > 0 {
		raw, err := json.Marshal(sample.JudgeErrors)
		if err != nil {
			// Cannot happen for []string, but dropping the row over an
			// unmarshalable error list would lose the scores too.
			return fmt.Errorf("ragassamples: marshal judge errors: %w", err)
		}
		errsJSON = string(raw)
	}

	var sampledAt any
	if !sample.SampledAt.IsZero() {
		sampledAt = sample.SampledAt
	}

	_, err := s.pool.Exec(ctx, insertSQL,
		nullableID(sample.MessageID),
		nullableID(sample.KbID),
		sample.Faithfulness,
		sample.AnswerRelevance,
		sample.ContextPrecision,
		sample.Coverage,
		sample.JudgeModel,
		errsJSON,
		sampledAt,
	)
	if err != nil {
		return fmt.Errorf("ragassamples: insert: %w", err)
	}
	return nil
}

// dailyStatsSQL aggregates one window per KB.
//
// AVG() skips NULLs, which is the whole point: a KB whose faithfulness prompt
// timed out on half its samples reports the mean of the half that succeeded,
// and reports NULL (→ no gauge series) when none did. COUNT(*) counts every
// sample including the failed ones, so N is the sampling volume, not the
// metric's denominator.
const dailyStatsSQL = `
	SELECT kb_id::text,
	       COUNT(*)::int,
	       AVG(faithfulness)::float8,
	       AVG(answer_relevance)::float8,
	       AVG(context_precision)::float8
	  FROM ragas_samples
	 WHERE kb_id IS NOT NULL AND sampled_at >= $1
	 GROUP BY kb_id`

// DailyStats aggregates the window starting at since, keyed by KB id.
func (s *PGStore) DailyStats(ctx context.Context, since time.Time) (map[string]DailyStats, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, dailyStatsSQL, since)
	if err != nil {
		return nil, fmt.Errorf("ragassamples: daily stats: %w", err)
	}
	defer rows.Close()

	out := make(map[string]DailyStats)
	for rows.Next() {
		var kbID string
		var st DailyStats
		if err := rows.Scan(&kbID, &st.N, &st.Faithfulness, &st.AnswerRelevance, &st.ContextPrecision); err != nil {
			return nil, fmt.Errorf("ragassamples: scan daily stats: %w", err)
		}
		out[kbID] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ragassamples: iterate daily stats: %w", err)
	}
	return out, nil
}

// Prune deletes samples older than olderThan. Rows whose KB or message has
// since been deleted (both FKs are ON DELETE SET NULL) are pruned by age like
// any other row — retention is what bounds the table, not the FKs.
func (s *PGStore) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM ragas_samples WHERE sampled_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("ragassamples: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}
