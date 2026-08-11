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

// MessageStats are the per-KB chat/message aggregates.
type MessageStats struct {
	MessageCount  int
	ChatCount     int
	LastMessageAt *string
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
	MessageCount        int     `json:"messageCount"`
	ChatCount           int     `json:"chatCount"`
	LastFileUploadAt    *string `json:"lastFileUploadAt,omitempty"`
	LastMessageAt       *string `json:"lastMessageAt,omitempty"`
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
	MessageStatsByKB(ctx context.Context) (map[string]MessageStats, error)
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
	msgStats, err := s.store.MessageStatsByKB(ctx)
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
		if ms, ok := msgStats[kb.ID]; ok {
			row.MessageCount = ms.MessageCount
			row.ChatCount = ms.ChatCount
			row.LastMessageAt = ms.LastMessageAt
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
