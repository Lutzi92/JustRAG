package worker

import (
	"context"
	"runtime"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// Default queue priorities.
var defaultQueues = map[string]int{
	jobs.QueueQuick: 6, // highest priority
	jobs.QueueHeavy: 3,
	jobs.QueueBatch: 1, // lowest priority
}

// ServerConfig holds configuration for the Asynq worker server.
type ServerConfig struct {
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	RedisPoolSize int      // 0 = default (32)
	Queues        []string // if empty, processes all queues
	Concurrency   int      // 0 = default (8)
}

// NewServer creates an Asynq server. If cfg.Queues is set, only those queues
// are processed (enables deploying separate worker pods per queue type on K8s).
func NewServer(cfg ServerConfig) *asynq.Server {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		// Default to 2× GOMAXPROCS for I/O-bound ingestion tasks (LLM
		// API calls, DB inserts). On a single-core dev box this collapses
		// to the historical floor of 8 so behaviour doesn't regress;
		// on a 32-core ingestion host this lets the worker pool actually
		// saturate the connection pool and provider rate limits.
		concurrency = max(runtime.GOMAXPROCS(0)*2, 8)
	}
	poolSize := cfg.RedisPoolSize
	if poolSize <= 0 {
		poolSize = 32
	}

	queues := defaultQueues
	if len(cfg.Queues) > 0 {
		queues = make(map[string]int, len(cfg.Queues))
		for _, q := range cfg.Queues {
			if pri, ok := defaultQueues[q]; ok {
				queues[q] = pri
			} else {
				queues[q] = 1 // unknown queue gets lowest priority
			}
		}
	}

	return asynq.NewServer(
		asynq.RedisClientOpt{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
			PoolSize: poolSize,
		},
		asynq.Config{
			Queues:      queues,
			Concurrency: concurrency,
			// Default is 8s — too short for an in-flight embedding/ingest
			// task that may be mid-LLM-call. On SIGTERM, asynq stops accepting
			// new tasks immediately and waits up to this deadline for active
			// handlers to return before forcibly cancelling. 30s matches the
			// k8s preStop terminationGracePeriodSeconds we target.
			ShutdownTimeout: 30 * time.Second,
			ErrorHandler:    asynq.ErrorHandlerFunc(handleTaskError),
		},
	)
}

// handleTaskError is the asynq server-level error hook, called after every
// failed handler invocation (including the final one before the task is
// moved to the asynq archive). Per-task handlers already log their own
// errors; this hook exists so retry EXHAUSTION is loud — without it an
// exhausted task lands in the Redis archive with no log line or metric
// distinguishing "will retry" from "gone until an operator runs the asynq
// CLI". File-processing tasks additionally mark their file status=error via
// MarkErrorOnExhaustion; this hook covers every other task type too.
func handleTaskError(ctx context.Context, task *asynq.Task, err error) {
	retried, _ := asynq.GetRetryCount(ctx)
	maxRetry, _ := asynq.GetMaxRetry(ctx)
	queue, _ := asynq.GetQueueName(ctx)
	if retried >= maxRetry {
		observability.RecordWorkerTaskExhausted(task.Type())
		logctx.From(ctx).Error("worker: task exhausted retries, archived to asynq dead-letter set",
			"task_type", task.Type(), "queue", queue,
			"retried", retried, "max_retry", maxRetry, "error", err)
		return
	}
	logctx.From(ctx).Warn("worker: task failed, will retry",
		"task_type", task.Type(), "queue", queue,
		"retried", retried, "max_retry", maxRetry, "error", err)
}

// NewClient creates an Asynq client for enqueuing jobs.
func NewClient(redisAddr, redisPassword string, redisDB, redisPoolSize int) *asynq.Client {
	if redisPoolSize <= 0 {
		redisPoolSize = 32
	}
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
		PoolSize: redisPoolSize,
	})
}
