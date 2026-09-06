package observability

import (
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ragasDailyMean / ragasDailyN publish the nightly per-KB aggregate over the
// ragas_samples table (migration 0072). The existing rag_ragas_*_score
// histograms are deployment-wide: they answer "did faithfulness drop" but not
// "in which KB". These gauges answer the second question without the
// per-sample cardinality a labelled histogram would cost.
//
// Both are refreshed once per nightly maintenance pass as a full SNAPSHOT of
// "the KBs sampled in the last 24 h" — see ResetRagasDaily.
var (
	ragasDailyMean = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rag_ragas_daily_mean",
			Help: "Mean RAGAS judge score per KB over the last 24 h, from the " +
				"ragas_samples table. Averages only samples where that judge " +
				"prompt succeeded — a failed prompt is absent, never 0. " +
				"Labels: metric (faithfulness/answer_relevance/" +
				"context_precision, closed enum), kb (capped, see the " +
				"overflow label). The overflow series' VALUE is not " +
				"meaningful: every KB past the cap writes to that one " +
				"series, so its mean is last-write-wins across them. Only " +
				"the series' PRESENCE carries information (the cap bit); " +
				"per-KB numbers for those KBs live in the admin KB overview.",
			ConstLabels: commonLabels,
		},
		[]string{"kb", "metric"},
	)

	ragasDailyN = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rag_ragas_daily_n",
			Help: "Number of RAGAS samples judged per KB over the last 24 h. " +
				"Counts every sample including ones whose judge failed, so it " +
				"is the sampling volume — NOT the denominator of " +
				"rag_ragas_daily_mean. Label: kb (capped, see the overflow label).",
			ConstLabels: commonLabels,
		},
		[]string{"kb"},
	)
)

// ragasDailyKnownMetrics is the closed enum for the metric label. Anything
// else folds to "other" rather than minting a series for a typo.
var ragasDailyKnownMetrics = map[string]bool{
	"faithfulness":      true,
	"answer_relevance":  true,
	"context_precision": true,
}

// ragasDailyMaxKBs caps the number of distinct kb label values these gauges
// will ever emit, mirroring sourceSyncAgeMaxKBs. Both gauges share one budget
// so a KB can never appear under its own name on one and under "overflow" on
// the other — a mean with no matching n is unreadable.
const ragasDailyMaxKBs = 500

var (
	ragasDailyKBSeen  sync.Map // kb → struct{}
	ragasDailyKBCount atomic.Int64
)

// ragasDailyKBLabel resolves a KB id to its label value, folding everything
// past the cap into "overflow". Per-KB resolution for the folded ones lives
// in the admin KB overview, which has no cardinality budget to respect.
func ragasDailyKBLabel(kbID string) string {
	if _, seen := ragasDailyKBSeen.Load(kbID); seen {
		return kbID
	}
	if ragasDailyKBCount.Load() >= ragasDailyMaxKBs {
		return "overflow"
	}
	if _, loaded := ragasDailyKBSeen.LoadOrStore(kbID, struct{}{}); !loaded {
		ragasDailyKBCount.Add(1)
	}
	return kbID
}

// SetRagasDailyMean records one KB's mean for one judge metric. Callers must
// skip metrics with no successful sample rather than passing 0 — the gauge
// has no way to distinguish "scored zero" from "never scored".
func SetRagasDailyMean(kbID, metric string, mean float64) {
	if !ragasDailyKnownMetrics[metric] {
		metric = "other"
	}
	ragasDailyMean.WithLabelValues(ragasDailyKBLabel(kbID), metric).Set(mean)
}

// SetRagasDailyN records how many samples a KB contributed to the window.
func SetRagasDailyN(kbID string, n float64) {
	ragasDailyN.WithLabelValues(ragasDailyKBLabel(kbID)).Set(n)
}

// ResetRagasDaily drops every series of both gauges (and the cardinality-cap
// bookkeeping). The nightly pass MUST call this before republishing, because
// these are a full snapshot of "the KBs sampled in the last 24 h": a Set-only
// refresh would leave a KB that stopped being sampled — or was deleted —
// frozen at its last mean forever, so a quality alert raised on it could
// never resolve.
//
// Clearing the cap bookkeeping with it is deliberate: the setters are only
// ever called from that snapshot pass, so recounting per pass keeps the cap
// bounding live KBs rather than every KB seen since boot.
func ResetRagasDaily() {
	ragasDailyMean.Reset()
	ragasDailyN.Reset()
	ragasDailyKBSeen.Range(func(k, _ any) bool {
		ragasDailyKBSeen.Delete(k)
		return true
	})
	ragasDailyKBCount.Store(0)
}

// RagasDailyMeanForTest exposes the mean gauge for test assertions.
func RagasDailyMeanForTest() *prometheus.GaugeVec { return ragasDailyMean }

// RagasDailyNForTest exposes the sample-count gauge for test assertions.
func RagasDailyNForTest() *prometheus.GaugeVec { return ragasDailyN }
