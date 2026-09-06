// Package adminkboverview serves the admin "KB Overview" panel: one row per
// knowledge base with file/storage/message/chat counts, ingestion-health
// counters, and last-activity timestamps, plus a global Asynq queue summary.
//
// Read path: GET /api/admin/kb-overview (admin + superadmin). Data is computed
// live from three GROUP BY aggregates on the main Postgres pool — no caching,
// no migration. KB counts are bounded, so a handful of indexed aggregates is
// cheap.
package adminkboverview

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
)

// monitoredQueues mirrors systemhealth: the ingestion queues we report depth for.
var monitoredQueues = []string{jobs.QueueQuick, jobs.QueueHeavy, jobs.QueueBatch}

// KBBase is the per-KB metadata row before stats are merged in.
type KBBase struct {
	ID            string  `db:"id"`
	Name          string  `db:"name"`
	OwnerName     *string `db:"owner_name"`
	OwnerID       *string `db:"owner_id"`
	OwnerUsername *string `db:"owner_username"`
	IsGlobal      bool    `db:"is_global"`
	IsPublished   bool    `db:"is_published"`
	CreatedAt     string  `db:"created_at"`
}

// FileStats are the per-KB file aggregates.
type FileStats struct {
	FileCount           int
	TotalSizeBytes      int64
	FailedFileCount     int
	ProcessingFileCount int
	LastFileUploadAt    *string
	// OldestFileAt is MIN(effective date) over the KB's files — how far back
	// the corpus reaches. Effective date = COALESCE(published_at, created_at).
	OldestFileAt *string
	// StaleFileCount counts files whose effective date is older than the
	// kb_stale_days threshold the service passes into the query.
	StaleFileCount int
}

// SyncStats are the per-KB source-sync aggregates, unioned over the three
// source tables (RSS feeds, Confluence spaces, git repositories).
type SyncStats struct {
	// LastSuccessAt is the newest verified success across the KB's sources
	// (migration 0071). Nil for a KB whose sources have not succeeded since
	// the column was added — no backfill was possible, so "unknown" is the
	// honest value.
	LastSuccessAt *string
	// LastAttemptAt is the newest ATTEMPT (last_polled_at / last_synced_at),
	// which a failing sync also refreshes. Used only as the display fallback
	// when LastSuccessAt is nil (W3-R10).
	LastAttemptAt *string
	// Failing is true when any of the KB's sources has consecutive_failures > 0.
	Failing bool
	// Kinds lists the source kinds the KB actually has ("rss", "confluence",
	// "git") so the UI can label the timestamp.
	Kinds []string
	// ByKind is the per-source-kind breakdown (W4-R9): one entry per kind the
	// KB actually has sources of. A KB whose RSS feed is healthy but whose
	// git source has never succeeded must not read as fully synced just
	// because the aggregate LastSuccessAt above picks up the RSS success.
	ByKind []SyncKindStatus
}

// SyncKindStatus is the sync status of one source kind ("rss", "confluence",
// "git") within a KB. LastSyncAt follows the same success-preferred-over-
// attempt fallback as the aggregate: SyncSucceeded says which of the two it
// is, so a fallback timestamp cannot be mistaken for a verified success.
type SyncKindStatus struct {
	Kind          string  `json:"kind"`
	LastSyncAt    *string `json:"lastSyncAt,omitempty"`
	SyncSucceeded bool    `json:"syncSucceeded"`
	SyncFailing   bool    `json:"syncFailing"`
	SourceCount   int     `json:"sourceCount"`
}

// SiteConfigReader reads one global site_config value. This package needs
// exactly one key — kb_stale_days, which is global-only: no per-KB registry
// entry and no overlay (W3-R12).
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Staleness threshold bounds. The clamp exists because a zero or negative
// value would silently mark every file stale (NOW() - 0 days) and an
// unbounded one would silently mark nothing stale.
const (
	staleDaysKey     = "kb_stale_days"
	defaultStaleDays = 180
	minStaleDays     = 1
	maxStaleDays     = 3650
)

// resolveStaleDays reads kb_stale_days, falling back to the default on a nil
// reader, a missing or blank value, an unparseable one, or a read error — an
// admin panel must still render when site_configs is unreachable.
func resolveStaleDays(ctx context.Context, cfg SiteConfigReader) int {
	if cfg == nil {
		return defaultStaleDays
	}
	raw, err := cfg.GetSiteConfigValue(ctx, staleDaysKey)
	if err != nil {
		slog.Debug("kboverview: kb_stale_days read failed; using default", "error", err)
		return defaultStaleDays
	}
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return defaultStaleDays
	}
	n, err := strconv.Atoi(strings.TrimSpace(*raw))
	if err != nil {
		slog.Warn("kboverview: kb_stale_days is not an integer; using default", "value", *raw)
		return defaultStaleDays
	}
	if n < minStaleDays {
		return minStaleDays
	}
	if n > maxStaleDays {
		return maxStaleDays
	}
	return n
}

// ChatStats is the per-KB chat aggregate. Message counts moved to the usage
// ledger (TurnStats); this type survives only for the optional chatCount column.
type ChatStats struct {
	ChatCount int
}

// TurnStats are the per-KB usage-ledger aggregates. WebTurns and APITurns sum
// to the KB's Aktivität column; LastTurnAt feeds Letzte Aktivität, which was
// blind to API traffic while it read MAX(messages.created_at).
type TurnStats struct {
	WebTurns   int
	APITurns   int
	LastTurnAt *string
}

// QueueStats holds queue depth counters (mirrors systemhealth.QueueStats).
type QueueStats struct {
	Waiting int `json:"waiting"`
	Active  int `json:"active"`
	Failed  int `json:"failed"`
}

// KBRow is one row of the rendered table.
type KBRow struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	OwnerName           *string `json:"ownerName,omitempty"`
	OwnerID             *string `json:"ownerId,omitempty"`
	OwnerUsername       *string `json:"ownerUsername,omitempty"`
	IsGlobal            bool    `json:"isGlobal"`
	IsPublished         bool    `json:"isPublished"`
	FileCount           int     `json:"fileCount"`
	TotalSizeBytes      int64   `json:"totalSizeBytes"`
	FailedFileCount     int     `json:"failedFileCount"`
	ProcessingFileCount int     `json:"processingFileCount"`
	ChatCount           int     `json:"chatCount"`
	WebTurns            int     `json:"webTurns"`
	APITurns            int     `json:"apiTurns"`
	LastFileUploadAt    *string `json:"lastFileUploadAt,omitempty"`
	LastTurnAt          *string `json:"lastTurnAt,omitempty"`
	CreatedAt           string  `json:"createdAt"`

	// Freshness columns (Wave 3 / Task 5). OldestFileAt is the age of the
	// corpus, StaleFileCount/StaleShare how much of it is older than
	// kb_stale_days (StaleShare is a 0..1 fraction, 0 for an empty KB).
	OldestFileAt   *string `json:"oldestFileAt,omitempty"`
	StaleFileCount int     `json:"staleFileCount"`
	StaleShare     float64 `json:"staleShare"`
	// LastSyncAt is the newest successful source sync, falling back to the
	// newest attempt when no success is recorded yet. SyncFailing flags a
	// source with consecutive failures; SyncKinds names the source kinds
	// this KB has (empty for a KB with no external sources, where
	// LastSyncAt is nil).
	//
	// SyncSucceeded (W4-R9) means "every kind that has sources has a
	// verified success" — NOT "at least one has", which is what it meant
	// through Wave 3. Under the old ANY semantics one healthy RSS feed
	// could mask a git source that has never synced; the per-kind
	// breakdown in SyncByKind is what the FE needs to show that source
	// specifically instead of the KB's best kind.
	LastSyncAt    *string          `json:"lastSyncAt,omitempty"`
	SyncSucceeded bool             `json:"syncSucceeded"`
	SyncFailing   bool             `json:"syncFailing"`
	SyncKinds     []string         `json:"syncKinds,omitempty"`
	SyncByKind    []SyncKindStatus `json:"syncByKind,omitempty"`
}

// OverviewResponse is the JSON returned by GET /api/admin/kb-overview.
type OverviewResponse struct {
	Rows         []KBRow               `json:"rows"`
	QueueSummary map[string]QueueStats `json:"queueSummary"`
	Timestamp    string                `json:"timestamp"`
	// StaleDays is the threshold StaleFileCount/StaleShare were computed
	// against, echoed so the UI can label the column instead of hardcoding
	// a number that an operator may have changed.
	StaleDays int `json:"staleDays"`
}

// Store is the data dependency. Each method is a single aggregate query.
type Store interface {
	ListKBs(ctx context.Context) ([]KBBase, error)
	FileStatsByKB(ctx context.Context, staleDays int) (map[string]FileStats, error)
	ChatStatsByKB(ctx context.Context) (map[string]ChatStats, error)
	TurnStatsByKB(ctx context.Context) (map[string]TurnStats, error)
	SyncStatsByKB(ctx context.Context) (map[string]SyncStats, error)
}

// queueInspector is the subset of *asynq.Inspector we use (for testability).
type queueInspector interface {
	GetQueueInfo(qname string) (*asynq.QueueInfo, error)
}

// Service builds the overview payload.
type Service struct {
	store     Store
	inspector queueInspector
	cfg       SiteConfigReader
}

// NewService creates a Service. inspector may be nil (queue summary degrades to zeros).
func NewService(store Store, inspector queueInspector) *Service {
	return &Service{store: store, inspector: inspector}
}

// SetSiteConfig injects the global site_config reader used for kb_stale_days.
// Optional — without it the staleness threshold is the documented default.
func (s *Service) SetSiteConfig(cfg SiteConfigReader) { s.cfg = cfg }

// Overview computes the full payload: per-KB rows merged from three aggregates,
// plus the global queue summary.
func (s *Service) Overview(ctx context.Context) (OverviewResponse, error) {
	kbs, err := s.store.ListKBs(ctx)
	if err != nil {
		return OverviewResponse{}, err
	}
	staleDays := resolveStaleDays(ctx, s.cfg)
	fileStats, err := s.store.FileStatsByKB(ctx, staleDays)
	if err != nil {
		return OverviewResponse{}, err
	}
	chatStats, err := s.store.ChatStatsByKB(ctx)
	if err != nil {
		return OverviewResponse{}, err
	}
	turnStats, err := s.store.TurnStatsByKB(ctx)
	if err != nil {
		return OverviewResponse{}, err
	}
	syncStats, err := s.store.SyncStatsByKB(ctx)
	if err != nil {
		return OverviewResponse{}, err
	}

	rows := make([]KBRow, 0, len(kbs))
	for _, kb := range kbs {
		row := KBRow{
			ID:            kb.ID,
			Name:          kb.Name,
			OwnerName:     kb.OwnerName,
			OwnerID:       kb.OwnerID,
			OwnerUsername: kb.OwnerUsername,
			IsGlobal:      kb.IsGlobal,
			IsPublished:   kb.IsPublished,
			CreatedAt:     kb.CreatedAt,
		}
		if fs, ok := fileStats[kb.ID]; ok {
			row.FileCount = fs.FileCount
			row.TotalSizeBytes = fs.TotalSizeBytes
			row.FailedFileCount = fs.FailedFileCount
			row.ProcessingFileCount = fs.ProcessingFileCount
			row.LastFileUploadAt = fs.LastFileUploadAt
			row.OldestFileAt = fs.OldestFileAt
			row.StaleFileCount = fs.StaleFileCount
			// Guard the division: a KB with no files has no stale share,
			// and float64(0)/float64(0) is NaN — which encoding/json
			// refuses to marshal, i.e. one empty KB would 500 the whole
			// admin panel.
			if fs.FileCount > 0 {
				row.StaleShare = float64(fs.StaleFileCount) / float64(fs.FileCount)
			}
		}
		if cs, ok := chatStats[kb.ID]; ok {
			row.ChatCount = cs.ChatCount
		}
		if ts, ok := turnStats[kb.ID]; ok {
			row.WebTurns = ts.WebTurns
			row.APITurns = ts.APITurns
			row.LastTurnAt = ts.LastTurnAt
		}
		if ss, ok := syncStats[kb.ID]; ok {
			row.SyncFailing = ss.Failing
			row.SyncKinds = ss.Kinds
			row.SyncByKind = ss.ByKind
			// Prefer the verified success; fall back to the last attempt
			// only when no success is recorded (pre-0071 rows, or a source
			// that has never succeeded).
			if ss.LastSuccessAt != nil {
				row.LastSyncAt = ss.LastSuccessAt
			} else {
				row.LastSyncAt = ss.LastAttemptAt
			}
			// SyncSucceeded is "every kind that has sources has a
			// verified success" (W4-R9) — computed from the per-kind
			// breakdown, not from the aggregate LastSuccessAt above,
			// which only proves ONE kind succeeded.
			row.SyncSucceeded = allSyncKindsSucceeded(ss.ByKind)
		}
		rows = append(rows, row)
	}

	return OverviewResponse{
		Rows:         rows,
		QueueSummary: s.queueSummary(),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		StaleDays:    staleDays,
	}, nil
}

// allSyncKindsSucceeded reports whether every kind in byKind has a verified
// success. A KB with no external sources (empty byKind) is NOT "succeeded" —
// there is nothing to have succeeded, so the aggregate stays false, matching
// the pre-W4-R9 behaviour for a KB with no sync stats row at all.
//
// Mutation: revert this to "at least one kind succeeded" (the pre-W4-R9 ANY
// semantics) → a KB with a healthy RSS feed and a never-succeeded git source
// reports SyncSucceeded=true again, which is exactly the masking bug W4-R9
// exists to fix.
func allSyncKindsSucceeded(byKind []SyncKindStatus) bool {
	if len(byKind) == 0 {
		return false
	}
	for _, k := range byKind {
		if !k.SyncSucceeded {
			return false
		}
	}
	return true
}

// queueSummary reads Asynq queue depths; any failure degrades that queue to zeros.
func (s *Service) queueSummary() map[string]QueueStats {
	out := make(map[string]QueueStats, len(monitoredQueues))
	for _, name := range monitoredQueues {
		if s.inspector == nil {
			out[name] = QueueStats{}
			continue
		}
		info, err := s.inspector.GetQueueInfo(name)
		if err != nil || info == nil {
			slog.Debug("kboverview: failed to get queue info", "queue", name, "error", err)
			out[name] = QueueStats{}
			continue
		}
		out[name] = QueueStats{Waiting: info.Pending, Active: info.Active, Failed: info.Archived}
	}
	return out
}
