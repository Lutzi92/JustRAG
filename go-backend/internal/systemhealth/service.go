package systemhealth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/jobs"
)

// SubsystemHealth represents the health state of an individual subsystem.
type SubsystemHealth struct {
	Status    string `json:"status"`              // "healthy", "unhealthy", "degraded"
	LatencyMs *int64 `json:"latencyMs,omitempty"` //nolint:tagliatelle
	Error     string `json:"error,omitempty"`
}

// QueueStats holds queue depth counters.
type QueueStats struct {
	Waiting int `json:"waiting"`
	Active  int `json:"active"`
	Failed  int `json:"failed"`
}

// ResourceUsage contains process and system resource metrics.
// Field names must match the Node.js response shape consumed by SystemHealthDashboard.tsx.
type ResourceUsage struct {
	NodeHeapUsedMB          int     `json:"nodeHeapUsedMB"`
	SystemMemoryUsedPercent int     `json:"systemMemoryUsedPercent"`
	CpuLoadAvg1m            float64 `json:"cpuLoadAvg1m"`
	CpuLoadAvg5m            float64 `json:"cpuLoadAvg5m"`
	CpuLoadAvg15m           float64 `json:"cpuLoadAvg15m"`
	CpuCount                int     `json:"cpuCount"`
}

// LiveMetrics is the full payload returned by GET /api/system-health/live.
type LiveMetrics struct {
	ActiveUsers         int                        `json:"activeUsers"`
	ProcessingFiles     int                        `json:"processingFiles"`
	TotalKnowledgeBases int                        `json:"totalKnowledgeBases"`
	TotalFiles          int                        `json:"totalFiles"`
	TotalStorageBytes   int64                      `json:"totalStorageBytes"`
	TotalUsers          int                        `json:"totalUsers"`
	QueueStats          map[string]QueueStats      `json:"queueStats"`
	ResourceUsage       ResourceUsage              `json:"resourceUsage"`
	SubsystemHealth     map[string]SubsystemHealth `json:"subsystemHealth"`
	Timestamp           string                     `json:"timestamp"`
}

// HistoricalPoint is a single time-series data point.
type HistoricalPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

// HistoricalMetrics wraps the metric name and its historical data points.
type HistoricalMetrics struct {
	Metric string            `json:"metric"`
	Data   []HistoricalPoint `json:"data"`
}

// MetricsStore abstracts DB queries so the service can be unit-tested with a mock.
type MetricsStore interface {
	GetActiveUsersCount(ctx context.Context) (int, error)
	GetProcessingFilesCount(ctx context.Context) (int, error)
	GetTotalKBsCount(ctx context.Context) (int, error)
	GetTotalFilesCount(ctx context.Context) (int, error)
	GetTotalStorageBytes(ctx context.Context) (int64, error)
	GetTotalUsersCount(ctx context.Context) (int, error)
	GetHistoricalMetrics(ctx context.Context, metric string, from, to time.Time) ([]HistoricalPoint, error)
}

// Service provides system-health business logic.
type Service struct {
	store     MetricsStore
	mainDB    *pgxpool.Pool
	vectorDB  *pgxpool.Pool
	rdb       *redis.Client
	inspector *asynq.Inspector
	s3Enabled bool
}

// NewService constructs a Service. All pool/client arguments may be nil for
// graceful degradation (subsystem checks will report "unhealthy").
func NewService(store MetricsStore, mainDB, vectorDB *pgxpool.Pool, rdb *redis.Client, inspector *asynq.Inspector, s3Enabled bool) *Service {
	return &Service{
		store:     store,
		mainDB:    mainDB,
		vectorDB:  vectorDB,
		rdb:       rdb,
		inspector: inspector,
		s3Enabled: s3Enabled,
	}
}

// monitoredQueues is the ordered list of Asynq queue names we monitor.
var monitoredQueues = []string{jobs.QueueQuick, jobs.QueueHeavy, jobs.QueueBatch}

// GetLiveMetrics collects all live metrics and returns them.
// Returns an error if any core DB query fails, so the handler can surface it as 500.
func (s *Service) GetLiveMetrics(ctx context.Context) (*LiveMetrics, error) {
	activeUsers, err := s.store.GetActiveUsersCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("active users: %w", err)
	}
	processingFiles, err := s.store.GetProcessingFilesCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("processing files: %w", err)
	}
	totalKBs, err := s.store.GetTotalKBsCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("total KBs: %w", err)
	}
	totalFiles, err := s.store.GetTotalFilesCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("total files: %w", err)
	}
	totalStorage, err := s.store.GetTotalStorageBytes(ctx)
	if err != nil {
		return nil, fmt.Errorf("total storage: %w", err)
	}
	totalUsers, err := s.store.GetTotalUsersCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("total users: %w", err)
	}

	queueStats := s.getQueueStats(ctx)
	subsystems := s.GetSubsystems(ctx)
	resources := s.getResourceUsage()

	return &LiveMetrics{
		ActiveUsers:         activeUsers,
		ProcessingFiles:     processingFiles,
		TotalKnowledgeBases: totalKBs,
		TotalFiles:          totalFiles,
		TotalStorageBytes:   totalStorage,
		TotalUsers:          totalUsers,
		QueueStats:          queueStats,
		ResourceUsage:       resources,
		SubsystemHealth:     subsystems,
		Timestamp:           time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// GetHistoricalMetrics delegates to the store.
func (s *Service) GetHistoricalMetrics(ctx context.Context, metric string, from, to time.Time) (*HistoricalMetrics, error) {
	points, err := s.store.GetHistoricalMetrics(ctx, metric, from, to)
	if err != nil {
		return nil, err
	}
	return &HistoricalMetrics{Metric: metric, Data: points}, nil
}

// GetSubsystems checks each subsystem and returns a health map.
// Keys must match the Node.js response: mainDb, vectorDb, redis, storage.
func (s *Service) GetSubsystems(ctx context.Context) map[string]SubsystemHealth {
	result := make(map[string]SubsystemHealth)

	result["mainDb"] = s.pingPool(ctx, s.mainDB)
	result["vectorDb"] = s.pingPool(ctx, s.vectorDB)
	result["redis"] = s.pingRedis(ctx)
	result["storage"] = s.checkStorageHealth()

	return result
}

// pingPool checks a pgxpool.Pool within a short timeout.
func (s *Service) pingPool(ctx context.Context, pool *pgxpool.Pool) SubsystemHealth {
	if pool == nil {
		msg := "not configured"
		return SubsystemHealth{Status: "unhealthy", Error: msg}
	}

	start := time.Now()
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		return SubsystemHealth{Status: "unhealthy", Error: "ping failed"}
	}

	ms := time.Since(start).Milliseconds()
	return SubsystemHealth{Status: "healthy", LatencyMs: &ms}
}

// pingRedis checks the Redis client within a short timeout.
func (s *Service) pingRedis(ctx context.Context) SubsystemHealth {
	if s.rdb == nil {
		return SubsystemHealth{Status: "unhealthy", Error: "not configured"}
	}

	start := time.Now()
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := s.rdb.Ping(pingCtx).Err(); err != nil {
		return SubsystemHealth{Status: "unhealthy", Error: "ping failed"}
	}

	ms := time.Since(start).Milliseconds()
	return SubsystemHealth{Status: "healthy", LatencyMs: &ms}
}

// getQueueStats reads Asynq queue depths via the Inspector API.
func (s *Service) getQueueStats(_ context.Context) map[string]QueueStats {
	stats := make(map[string]QueueStats, len(monitoredQueues))

	for _, name := range monitoredQueues {
		if s.inspector == nil {
			stats[name] = QueueStats{}
			continue
		}

		info, err := s.inspector.GetQueueInfo(name)
		if err != nil {
			slog.Debug("failed to get queue info", "queue", name, "error", err)
			stats[name] = QueueStats{}
			continue
		}

		stats[name] = QueueStats{
			Waiting: info.Pending,
			Active:  info.Active,
			Failed:  info.Archived,
		}
	}

	return stats
}

// checkStorageHealth checks local or S3 storage availability.
// For local storage, verifies read+write access (matching the Node.js check).
func (s *Service) checkStorageHealth() SubsystemHealth {
	if s.s3Enabled {
		// S3 configured — assume healthy if client initialized
		return SubsystemHealth{Status: "healthy"}
	}
	// Local storage: check data directory exists and is read+writable
	info, err := os.Stat("data")
	if err != nil {
		return SubsystemHealth{Status: "unhealthy", Error: err.Error()}
	}
	if !info.IsDir() {
		return SubsystemHealth{Status: "unhealthy", Error: "data path is not a directory"}
	}
	// Verify write access by creating and removing a temp file
	testFile := "data/.health-check-test"
	if err := os.WriteFile(testFile, []byte("ok"), 0644); err != nil {
		return SubsystemHealth{Status: "unhealthy", Error: "data directory is not writable: " + err.Error()}
	}
	os.Remove(testFile)
	return SubsystemHealth{Status: "healthy"}
}

// getResourceUsage collects Go heap stats and platform-specific system info.
func (s *Service) getResourceUsage() ResourceUsage {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	heapMB := int(mem.HeapInuse / 1024 / 1024)

	load := getLoadAvg()
	memPct := getSystemMemoryPercent()

	return ResourceUsage{
		NodeHeapUsedMB:          heapMB,
		SystemMemoryUsedPercent: memPct,
		CpuLoadAvg1m:            load[0],
		CpuLoadAvg5m:            load[1],
		CpuLoadAvg15m:           load[2],
		CpuCount:                runtime.NumCPU(),
	}
}
