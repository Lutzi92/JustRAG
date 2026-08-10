package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/redisclient"
	"github.com/justrag/go-backend/internal/storage"
)

// academicDeps composes chat.PGStore + files.PGStore to satisfy academic.AcademicStore.
type academicDeps struct {
	*chat.PGStore
	filesStore *files.PGStore
}

func (d *academicDeps) CreateFile(ctx context.Context, data files.CreateFileData) (*files.FileRecord, error) {
	return d.filesStore.CreateFile(ctx, data)
}

func (d *academicDeps) GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error) {
	return d.filesStore.GetKBByID(ctx, id)
}

func (d *academicDeps) GetKBShare(ctx context.Context, kbID, userID string) (*kbaccess.KBShare, error) {
	return d.filesStore.GetKBShare(ctx, kbID, userID)
}

// publicAPIDeps composes chat.PGStore + kb.PGStore to satisfy publicapi.Store.
type publicAPIDeps struct {
	*chat.PGStore
	kbStore *kb.PGStore
}

func (d *publicAPIDeps) ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]kb.KBRow, error) {
	return d.kbStore.ListKnowledgeBases(ctx, userID, limit, offset)
}

func (d *publicAPIDeps) GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error) {
	return d.PGStore.GetKBSystemPrompt(ctx, kbID)
}

// openaiDeps composes kb.PGStore + kbaccess.PGStore to satisfy openaicompat.Store.
type openaiDeps struct {
	*kb.PGStore
	kbAccessStore *kbaccess.PGStore
}

func (d *openaiDeps) GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error) {
	return d.kbAccessStore.GetKBByID(ctx, id)
}

func (d *openaiDeps) GetKBShare(ctx context.Context, kbID, userID string) (*kbaccess.KBShare, error) {
	return d.kbAccessStore.GetKBShare(ctx, kbID, userID)
}

// serverInfra holds all shared infrastructure connections created during server startup.
type serverInfra struct {
	db          *database.DB
	rdb         *redisclient.Client
	stor        storage.Storage
	asynqClient *asynq.Client
	blacklist   *auth.Blacklist
	// sqlToolDB is the dedicated SELECT-only pool the sql_query MCP tool runs
	// against. Nil when JUSTRAG_DB_URL_READONLY is unset or unreachable — the
	// tool is then registered in a disabled state rather than falling back to
	// the read/write main pool.
	sqlToolDB *pgxpool.Pool
}

// setupServerInfra creates database, Redis, storage, and asynq client connections.
// The caller is responsible for closing all resources via the returned cleanup function.
func setupServerInfra(ctx context.Context, cfg *config.Config) (*serverInfra, func(), error) {
	db, err := database.Connect(ctx, cfg.DB, cfg.VectorDB)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to database: %w", err)
	}

	// Surface pgxpool.Stat() to Prometheus. EmptyAcquireCount is the canary
	// for pool saturation. Registration failures are logged but non-fatal —
	// metrics being unavailable should not block startup.
	if err := observability.RegisterPgxPoolStats(db.Main, "main"); err != nil {
		slog.Warn("register main pgxpool stats", "err", err)
	}
	if db.Vector != nil && db.Vector != db.Main {
		if err := observability.RegisterPgxPoolStats(db.Vector, "vector"); err != nil {
			slog.Warn("register vector pgxpool stats", "err", err)
		}
	}

	rdb := redisclient.New(cfg.Redis)
	if err := rdb.Ping(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("connect to Redis: %w", err)
	}
	slog.Info("connected to Redis", "host", cfg.Redis.Host, "port", cfg.Redis.Port)

	// Surface go-redis PoolStats() to Prometheus. Misses / Timeouts are the
	// canary for Redis pool saturation; StaleConns surfaces churn. Registration
	// failures are logged but non-fatal — mirrors the pgxpool stats handling
	// above. The asynq client (below) has its own internal Redis pool that does
	// not expose PoolStats(), so we only register the shared go-redis pool here.
	if err := observability.RegisterRedisPoolStats(rdb, "main"); err != nil {
		slog.Warn("register Redis pool stats", "err", err)
	}

	stor, err := storage.New(storage.Config{
		S3Endpoint:  cfg.S3.Endpoint,
		S3AccessKey: cfg.S3.AccessKey,
		S3SecretKey: cfg.S3.SecretKey,
		S3Bucket:    cfg.S3.Bucket,
		S3Region:    cfg.S3.Region,
	})
	if err != nil {
		db.Close()
		rdb.Close()
		return nil, nil, fmt.Errorf("initialize storage: %w", err)
	}

	// asynq manages its own Redis connection pool (it cannot share one
	// with go-redis because it consumes the connection for blocking BRPOP
	// in the worker), so the effective Redis-side connection count is
	// REDIS_POOL_SIZE × 2: one pool for the shared go-redis client used
	// by HTTP handlers and one for asynq's internal scheduler/processor.
	// Operators sizing Redis maxclients should account for both.
	asynqPoolSize := cfg.Redis.PoolSize
	if asynqPoolSize <= 0 {
		asynqPoolSize = 32
	}
	asynqClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: asynqPoolSize,
	})

	blacklist := auth.NewBlacklist(rdb.NewBlacklistAdapter(), cfg.IsProduction())
	blacklist.RecordServerBoot(ctx)

	// Dedicated SELECT-only pool for the sql_query MCP tool. A bad or
	// unreachable read-only DSN is non-fatal: log and leave sqlToolDB nil so
	// the tool registers in its disabled state. Never reuse db.Main here —
	// that would expose the read/write pool to LLM-authored SQL.
	var sqlToolDB *pgxpool.Pool
	if cfg.SQLToolReadOnlyURL != "" {
		pool, err := database.ConnectReadOnly(ctx, cfg.SQLToolReadOnlyURL)
		switch {
		case err != nil:
			slog.Warn("sql_query read-only pool unavailable; sql_query tool will be disabled", "error", err)
		default:
			// The regex/keyword validator in the sql_query tool is only a second
			// line of defence — the database role is the real boundary, and
			// nothing verified the operator actually configured a least-privilege
			// one. Probe it at startup and fail closed (tool disabled) rather than
			// handing a writable or superuser pool to LLM-authored SQL.
			if verr := database.VerifyReadOnly(ctx, pool); verr != nil {
				slog.Error("sql_query read-only pool failed its least-privilege probe; sql_query tool will be disabled",
					"error", verr)
				pool.Close()
			} else {
				sqlToolDB = pool
				slog.Info("connected sql_query read-only pool (least-privilege probe passed)")
			}
		}
	}

	infra := &serverInfra{
		db:          db,
		rdb:         rdb,
		stor:        stor,
		asynqClient: asynqClient,
		blacklist:   blacklist,
		sqlToolDB:   sqlToolDB,
	}

	// Each defer runs even if a prior Close panics — that's why this uses
	// defers instead of plain sequential calls. Runtime order is reversed
	// from registration order (LIFO):
	//   executed first  → asynqClient.Close() (stop task dispatching)
	//   executed second → rdb.Close()         (Redis connections)
	//   executed last   → db.Close()          (DB connections)
	cleanup := func() {
		defer db.Close()          // registered first, runs last
		defer rdb.Close()         // registered second, runs middle
		defer asynqClient.Close() // registered last, runs first
		if sqlToolDB != nil {
			defer sqlToolDB.Close()
		}
	}

	return infra, cleanup, nil
}
