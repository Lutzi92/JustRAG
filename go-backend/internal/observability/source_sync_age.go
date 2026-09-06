package observability

import (
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// sourceSyncAge reports how long ago each KB source last synced
// SUCCESSFULLY (files.last_success_at, migration 0071), falling back to the
// last attempt for a source that has not succeeded since that column landed.
// Refreshed once per maintenance tick from the worker.
//
// Alert shape: a feed whose age climbs past its schedule interval is stale
// even while its last_polled_at keeps moving — that gap is exactly what this
// gauge exists to expose, and why it keys on the success column rather than
// the attempt one.
var sourceSyncAge = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "rag_source_sync_age_seconds",
		Help: "Seconds since a KB source last synced successfully " +
			"(last_success_at, falling back to the last attempt when the " +
			"source has never succeeded). Labels: kind (rss/confluence/" +
			"git/other), kb (capped, see the overflow label).",
		ConstLabels: commonLabels,
	},
	[]string{"kind", "kb"},
)

// sourceSyncAgeKnownKinds is the closed enum for the kind label. Anything
// else folds to "other" rather than minting a series for a typo.
var sourceSyncAgeKnownKinds = map[string]bool{"rss": true, "confluence": true, "git": true}

// sourceSyncAgeMaxKBs caps the number of distinct kb label values this gauge
// will ever emit, mirroring onlineFaithfulnessMaxKBs. This gauge is refreshed
// for EVERY KB with a source on every maintenance tick, so an unbounded label
// would grow Prometheus series with the KB count and never shrink.
const sourceSyncAgeMaxKBs = 500

var (
	sourceSyncAgeKBSeen  sync.Map // kb → struct{}
	sourceSyncAgeKBCount atomic.Int64
)

// SetSourceSyncAge records the age, in seconds, of a KB source's last
// successful sync. Negative values (clock skew between DB and app) clamp to
// zero. kbID is emitted as-is up to sourceSyncAgeMaxKBs distinct values, then
// folds into "overflow"; per-KB resolution for those lives in the admin KB
// overview, which has no cardinality budget to respect.
func SetSourceSyncAge(kind, kbID string, seconds float64) {
	if !sourceSyncAgeKnownKinds[kind] {
		kind = "other"
	}
	if seconds < 0 {
		seconds = 0
	}
	if _, seen := sourceSyncAgeKBSeen.Load(kbID); !seen {
		if sourceSyncAgeKBCount.Load() >= sourceSyncAgeMaxKBs {
			kbID = "overflow"
		} else if _, loaded := sourceSyncAgeKBSeen.LoadOrStore(kbID, struct{}{}); !loaded {
			sourceSyncAgeKBCount.Add(1)
		}
	}
	sourceSyncAge.WithLabelValues(kind, kbID).Set(seconds)
}

// SourceSyncAgeForTest exposes the gauge for test assertions.
func SourceSyncAgeForTest() *prometheus.GaugeVec { return sourceSyncAge }

// resetSourceSyncAgeCapForTest clears the seen-KB set so cap tests start from
// a known state. Test-only; the production path never resets the cap.
func resetSourceSyncAgeCapForTest() {
	sourceSyncAgeKBSeen.Range(func(k, _ any) bool {
		sourceSyncAgeKBSeen.Delete(k)
		return true
	})
	sourceSyncAgeKBCount.Store(0)
}
