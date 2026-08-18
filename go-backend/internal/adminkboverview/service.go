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
}

// OverviewResponse is the JSON returned by GET /api/admin/kb-overview.
type OverviewResponse struct {
	Rows         []KBRow               `json:"rows"`
	QueueSummary map[string]QueueStats `json:"queueSummary"`
	Timestamp    string                `json:"timestamp"`
}

// Store is the data dependency. Each method is a single aggregate query.
type Store interface {
	ListKBs(ctx context.Context) ([]KBBase, error)
	FileStatsByKB(ctx context.Context) (map[string]FileStats, error)
	ChatStatsByKB(ctx context.Context) (map[string]ChatStats, error)
	TurnStatsByKB(ctx context.Context) (map[string]TurnStats, error)
}

// queueInspector is the subset of *asynq.Inspector we use (for testability).
type queueInspector interface {
	GetQueueInfo(qname string) (*asynq.QueueInfo, error)
}

// Service builds the overview payload.
type Service struct {
	store     Store
	inspector queueInspector
}

// NewService creates a Service. inspector may be nil (queue summary degrades to zeros).
func NewService(store Store, inspector queueInspector) *Service {
	return &Service{store: store, inspector: inspector}
}

// Overview computes the full payload: per-KB rows merged from three aggregates,
// plus the global queue summary.
func (s *Service) Overview(ctx context.Context) (OverviewResponse, error) {
	kbs, err := s.store.ListKBs(ctx)
	if err != nil {
		return OverviewResponse{}, err
	}
	fileStats, err := s.store.FileStatsByKB(ctx)
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
		}
		if cs, ok := chatStats[kb.ID]; ok {
			row.ChatCount = cs.ChatCount
		}
		if ts, ok := turnStats[kb.ID]; ok {
			row.WebTurns = ts.WebTurns
			row.APITurns = ts.APITurns
			row.LastTurnAt = ts.LastTurnAt
		}
		rows = append(rows, row)
	}

	return OverviewResponse{
		Rows:         rows,
		QueueSummary: s.queueSummary(),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}, nil
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
