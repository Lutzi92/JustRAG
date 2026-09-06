// Package ragassamples persists the results of the RAGAS production sampler
// (internal/worker/ragas_sample.go) and reads them back as per-KB daily
// aggregates.
//
// Why a table at all: the sampler emitted Prometheus histograms and nothing
// else, so a judge score could be alerted on but never attributed. A
// histogram answers "faithfulness p50 dropped"; it cannot answer "in which
// KB", "on which answer", or "was the judge itself timing out that night".
// The histograms stay — they remain the alerting surface — and this package
// is the attribution surface behind them (migration 0072, W5-R6).
//
// Every score is a pointer and stays nil when that judge prompt failed.
// Persisting 0.0 for a failed prompt would poison every mean with false
// negatives, which is precisely the distinction RecordRAGASSample already
// makes for the histograms; JudgeErrors carries the reason so an all-nil row
// reads as "the judge broke", not "the answer was terrible".
package ragassamples

import (
	"context"
	"time"
)

// Sample is one judged production turn. MessageID and KbID are pointers
// because both are nullable in the table: a sampled turn on a surface that
// persists no message has neither, and the ON DELETE SET NULL foreign keys
// blank them when the message or KB is later deleted (the historical quality
// record must not shrink when a KB is removed).
//
// SampledAt is the caller's timestamp; a zero value lets the column default
// to now(). Coverage is always nil on the production-sampling path — the
// coverage judge needs a question's expected points, which a sampled
// production turn does not have — and exists for a future golden-point source.
type Sample struct {
	MessageID        *string
	KbID             *string
	Faithfulness     *float64
	AnswerRelevance  *float64
	ContextPrecision *float64
	Coverage         *float64
	JudgeModel       string
	JudgeErrors      []string
	SampledAt        time.Time
}

// DailyStats is one KB's aggregate over a window. N counts every sample in
// the window, including rows whose judge failed; each mean is nil when no row
// in the window carried that metric, and averages only the non-nil ones — so
// N is deliberately NOT the denominator of the means. Reporting a mean over a
// denominator that includes failed prompts is the same false-negative bug the
// nullable columns exist to prevent.
type DailyStats struct {
	N                int
	Faithfulness     *float64
	AnswerRelevance  *float64
	ContextPrecision *float64
}

// Store is the persistence surface the worker and the nightly maintenance
// pass depend on. Kept an interface so both can be unit-tested without a
// database, and so a deployment with no main pool can wire nil.
type Store interface {
	// Insert writes one judged sample.
	Insert(ctx context.Context, s Sample) error
	// DailyStats aggregates samples with sampled_at >= since, keyed by KB id.
	// Rows whose kb_id is NULL are skipped: they would otherwise mint a
	// gauge series under an empty label.
	DailyStats(ctx context.Context, since time.Time) (map[string]DailyStats, error)
	// Prune deletes samples older than olderThan and returns how many rows
	// went away.
	Prune(ctx context.Context, olderThan time.Time) (int64, error)
}
