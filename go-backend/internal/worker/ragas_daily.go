package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/ragassamples"
)

// ragasDailyWindow is the aggregation window of the nightly pass. It is fixed
// at 24 h rather than tied to the tick interval so the published gauges keep
// the meaning their names carry (`_daily_`) even if an operator shortens the
// interval for a debugging run.
const ragasDailyWindow = 24 * time.Hour

// ragasDefaultRetention mirrors the ragas_samples_retention_days default and
// applies when no reader is wired.
const ragasDefaultRetention = 90 * 24 * time.Hour

// ragasRetention resolves the retention window, falling back to the default
// when no reader is wired or the reader returns a non-positive duration — a
// zero would otherwise be read by Prune as "delete everything up to now".
func ragasRetention(ctx context.Context, read func(context.Context) time.Duration) time.Duration {
	if read == nil {
		return ragasDefaultRetention
	}
	if d := read(ctx); d > 0 {
		return d
	}
	return ragasDefaultRetention
}

// refreshRagasDaily is one nightly pass: republish the per-KB aggregate over
// the last 24 h, then delete samples past the retention window.
//
// Two ordering decisions worth keeping:
//
//   - The gauge vectors are reset only AFTER the aggregate query succeeds, so
//     a failed query leaves the previous snapshot intact (a day-old gauge
//     beats a blank one) — the same rule refreshSourceSyncAge follows.
//   - The prune runs even when the aggregate failed. Retention is a storage
//     bound, not a reporting feature; letting a broken dashboard query stop
//     the delete would let the table grow unboundedly for exactly as long as
//     nobody notices the gauges are stale.
func refreshRagasDaily(ctx context.Context, store ragassamples.Store, retention time.Duration) {
	if store == nil {
		return
	}
	now := time.Now()

	stats, err := store.DailyStats(ctx, now.Add(-ragasDailyWindow))
	if err != nil {
		slog.Error("ragas daily: aggregate query failed", "error", err)
	} else {
		observability.ResetRagasDaily()
		for kbID, st := range stats {
			observability.SetRagasDailyN(kbID, float64(st.N))
			// A nil mean means no sample in the window carried that metric
			// (every judge prompt for it failed). Publishing 0 would read as
			// "this KB answers nothing faithfully"; leaving the series absent
			// reads as what it is — no data.
			if st.Faithfulness != nil {
				observability.SetRagasDailyMean(kbID, "faithfulness", *st.Faithfulness)
			}
			if st.AnswerRelevance != nil {
				observability.SetRagasDailyMean(kbID, "answer_relevance", *st.AnswerRelevance)
			}
			if st.ContextPrecision != nil {
				observability.SetRagasDailyMean(kbID, "context_precision", *st.ContextPrecision)
			}
		}
		slog.Debug("ragas daily aggregate refreshed", "kbs", len(stats))
	}

	deleted, err := store.Prune(ctx, now.Add(-retention))
	if err != nil {
		slog.Error("ragas daily: prune failed", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("ragas daily: pruned old samples",
			"deleted", deleted, "retentionDays", int(retention.Hours()/24))
	}
}
