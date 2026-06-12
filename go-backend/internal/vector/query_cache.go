package vector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
)

// QueryCache is a pgvector-backed cache for full SearchResult payloads.
// Lookup is by (kb_id, shape_hash, query_embedding-cosine-similarity).
// Writes go through a bounded async channel so the chat path never blocks
// on the cache; on overflow, writes drop and increment
// rag_query_cache_write_dropped_total.
//
// NewQueryCache does NOT allocate the writeCh — only StartWriter does.
// This gives integration tests a deterministic synchronous-write path
// (construct cache, never call StartWriter, Store falls through to
// writeOne) while production callers get the async path by calling
// StartWriter at boot.
//
// writeCh is an atomic.Pointer because StartWriter publishes the channel
// from inside sync.Once while Store reads it from arbitrary goroutines.
// sync.Once only synchronizes between Do callers; readers that don't go
// through Do (i.e. Store) need their own happens-before edge, which the
// atomic Load/Store pair provides.
//
// Shutdown contract: there is no Stop() — the writer goroutine exits
// when the context passed to StartWriter is cancelled, and up to
// queryCacheWriteBuffer queued writes are intentionally dropped on
// shutdown. The cache is best-effort (Store() already drops on overflow)
// and a write carries no caller-visible state, so the next cache miss
// simply recomputes the embedding. Draining queued writes on shutdown
// would couple graceful exit to a 5s timeout per buffered entry — a
// worse tradeoff than the silent drop.
type QueryCache struct {
	pool *pgxpool.Pool

	writeCh atomic.Pointer[chan queryCacheWrite]
	once    sync.Once
	// dimSkipWarnOnce gates the one-shot operator warning emitted when a
	// query embedding's dimension doesn't match the fixed query_cache
	// column (see queryCacheDims). The per-event signal is the
	// rag_query_cache_dim_skipped_total counter.
	dimSkipWarnOnce sync.Once
}

// queryCacheDims is the dimension of the query_cache.query_embedding
// column — fixed at vector(2560) by migration 0011 (sized for the
// Qwen3-Embedding-4B deployment the cache shipped under). Embeddings of
// any other dimension can be neither stored nor compared: the
// ::halfvec(2560) cast raises a pgvector dimension error, which used to
// surface only as a swallowed per-lookup error and a per-write warn log.
// Lookup/Store skip the cache outright for mismatched dimensions and
// record rag_query_cache_dim_skipped_total so the silent-disable is
// visible on dashboards. Rebuilding the cache for a new embedder
// dimension requires recreating the query_cache table + HNSW index at
// that dimension.
const queryCacheDims = 2560

// dimSupported reports whether emb can be used against the fixed-dim
// query_cache column; on mismatch it records the skip metric and emits a
// one-shot warning.
func (c *QueryCache) dimSupported(ctx context.Context, emb []float64) bool {
	if len(emb) == queryCacheDims {
		return true
	}
	observability.RecordQueryCacheDimSkipped()
	c.dimSkipWarnOnce.Do(func() {
		logctx.From(ctx).Warn("query_cache: embedding dimension does not match query_cache column; cache disabled for this embedder",
			"embedding_dims", len(emb), "column_dims", queryCacheDims)
	})
	return false
}

type queryCacheWrite struct {
	kbID      string
	hash      []byte
	embedding []float64
	queryText string
	result    []byte
	ttl       time.Duration
}

const queryCacheWriteBuffer = 256

// NewQueryCache constructs a QueryCache backed by the vector DB pool.
// Call StartWriter once at app startup to launch the async insert loop.
// Without StartWriter, Store writes synchronously — useful for tests.
func NewQueryCache(pool *pgxpool.Pool) *QueryCache {
	return &QueryCache{pool: pool}
}

// StartWriter allocates the bounded write channel and launches the async
// writer goroutine. Idempotent — subsequent calls are no-ops. The
// goroutine exits when ctx is canceled.
func (c *QueryCache) StartWriter(ctx context.Context) {
	c.once.Do(func() {
		ch := make(chan queryCacheWrite, queryCacheWriteBuffer)
		c.writeCh.Store(&ch)
		// safego.Go isolates a panic in writeOne (nil pool, pgx driver
		// bug, etc.) so a background write failure can't crash the
		// process — the chat hot path already drops on overflow, that
		// best-effort contract should hold for panics too.
		safego.Go(func() { c.writeLoop(ctx, ch) })
	})
}

func (c *QueryCache) writeLoop(ctx context.Context, writeCh chan queryCacheWrite) {
	for {
		select {
		case <-ctx.Done():
			// Intentional: up to queryCacheWriteBuffer items in writeCh are
			// dropped on shutdown. The cache is best-effort (Store() already
			// drops on overflow + records rag_query_cache_write_dropped_total)
			// and writes carry no caller-visible state; the next cache miss
			// will recompute the embedding. Draining would tie graceful
			// shutdown to a 5s write timeout per buffered item.
			//
			// Log the queued count so a context cancelled unexpectedly early
			// (leaked cancel, misconfigured test harness) is not silently
			// invisible — under a clean shutdown the count is usually 0.
			if n := len(writeCh); n > 0 {
				logctx.From(ctx).Info("query_cache: writer shutting down, dropping queued writes", "dropped", n)
			}
			return
		case w := <-writeCh:
			c.processWrite(ctx, w)
		}
	}
}

// processWrite handles one queued write under a fresh per-call timeout.
// Extracted so defer cancel() fires per iteration — a defer inside the
// select case body would accumulate until writeLoop returns.
//
// The writeLoop's ctx is the application-lifetime context, not a request
// context, so it doesn't carry a trace span — but if a span did make it
// onto the loop ctx (test setups, future per-request workers), propagate
// it onto the detached write so the insert still appears in the trace.
func (c *QueryCache) processWrite(ctx context.Context, w queryCacheWrite) {
	span := trace.SpanFromContext(ctx)
	bg := trace.ContextWithSpan(context.Background(), span)
	writeCtx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	if err := c.writeOne(writeCtx, w); err != nil {
		logctx.From(ctx).Warn("query_cache: async write failed", "error", err, "kb_id", w.kbID)
	}
}

// Lookup returns the cached SearchResult bytes when an entry within the
// similarity threshold exists. On hit the bytes are non-nil and `hit=true`;
// on miss `hit=false` and `distance` is the nearest non-matching distance
// (useful for tuning the threshold).
//
// `threshold` is a cosine SIMILARITY threshold in [0, 1]; the underlying
// pgvector `<=>` operator returns cosine DISTANCE in [0, 2]. The hit
// criterion is `distance <= 1 - threshold`.
func (c *QueryCache) Lookup(ctx context.Context, kbID string, hash []byte, embedding []float64, threshold float64) (resultJSON []byte, hit bool, distance float64, err error) {
	if !c.dimSupported(ctx, embedding) {
		return nil, false, 0, nil
	}
	start := time.Now()
	defer func() { observability.QueryCacheLookupSeconds.Observe(time.Since(start).Seconds()) }()

	// Bind the query embedding as a binary pgvector parameter (registered
	// codec, see internal/database/pgvector_codec.go) instead of a text
	// literal — ~4× smaller wire payload per lookup and no formatEmbedding
	// pool borrow/return cycle. The `$3::vector` cast is a no-op on an
	// already-vector-typed param (matches the insertBatch convention) and
	// keeps the halfvec recast that the HNSW index requires.
	embArg := vectorArg(embedding)
	// The HNSW index is on (query_embedding::halfvec(2560)) halfvec_cosine_ops
	// (vector(2560) > pgvector's 2000-dim HNSW cap, halfvec extends it to 4000).
	// ORDER BY must use the same halfvec cast on both sides to hit the index.
	row := c.pool.QueryRow(ctx, `
		SELECT result_json, query_embedding::halfvec(2560) <=> $3::vector::halfvec(2560) AS distance
		FROM query_cache
		WHERE kb_id = $1
		  AND shape_hash = $2
		  AND expires_at > now()
		ORDER BY query_embedding::halfvec(2560) <=> $3::vector::halfvec(2560)
		LIMIT 1
	`, kbID, hash, embArg)

	var bytes []byte
	if err := row.Scan(&bytes, &distance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, 0, nil
		}
		return nil, false, 0, fmt.Errorf("query_cache lookup: %w", err)
	}

	if distance > (1.0 - threshold) {
		return nil, false, distance, nil
	}
	return bytes, true, distance, nil
}

// Store schedules an async cache insert. Returns immediately. On a full
// write buffer the call records a dropped-write metric and returns nil
// — the cache is best-effort. When called before StartWriter (i.e.
// writeCh is still nil), Store falls back to a synchronous insert so
// integration tests can drive the cache deterministically.
func (c *QueryCache) Store(ctx context.Context, kbID string, hash []byte, embedding []float64, queryText string, result []byte, ttl time.Duration) error {
	if !c.dimSupported(ctx, embedding) {
		return nil
	}
	w := queryCacheWrite{kbID: kbID, hash: hash, embedding: embedding, queryText: queryText, result: result, ttl: ttl}
	chPtr := c.writeCh.Load()
	if chPtr == nil {
		return c.writeOne(ctx, w)
	}
	select {
	case *chPtr <- w:
		return nil
	default:
		observability.RecordQueryCacheWriteDropped()
		logctx.From(ctx).Warn("query_cache: write buffer full, dropping cache write", "kb_id", kbID)
		return nil
	}
}

func (c *QueryCache) writeOne(ctx context.Context, w queryCacheWrite) error {
	// Binary pgvector binding, matching Lookup and insertBatch — see vectorArg.
	_, err := c.pool.Exec(ctx, `
		INSERT INTO query_cache (kb_id, shape_hash, query_embedding, query_text, result_json, expires_at)
		VALUES ($1, $2, $3::vector, $4, $5, now() + ($6 * interval '1 millisecond'))
	`, w.kbID, w.hash, vectorArg(w.embedding), w.queryText, w.result, w.ttl.Milliseconds())
	return err
}

// Invalidate deletes all cache entries for a knowledge base. Called by
// file mutation paths (ingest, delete, re-ingest). Idempotent — calling
// on a KB with no entries is a no-op that still increments the metric.
func (c *QueryCache) Invalidate(ctx context.Context, kbID string) error {
	_, err := c.pool.Exec(ctx, "DELETE FROM query_cache WHERE kb_id = $1", kbID)
	if err == nil {
		observability.RecordQueryCacheInvalidation()
	}
	return err
}

// SweepExpired runs a one-shot DELETE of all expired entries and refreshes
// the size gauge. Designed to be called from a periodic ticker.
func (c *QueryCache) SweepExpired(ctx context.Context) (deleted int64, err error) {
	tag, err := c.pool.Exec(ctx, "DELETE FROM query_cache WHERE expires_at < now()")
	if err != nil {
		return 0, err
	}
	deleted = tag.RowsAffected()

	// pg_class.reltuples is an approximate row count maintained by ANALYZE /
	// autovacuum. It avoids the full sequential scan that count(*) requires
	// on an unindexed predicate-less query. Accuracy is sufficient for a
	// Prometheus gauge; the value is refreshed on every ANALYZE.
	var size int64
	if err := c.pool.QueryRow(ctx, `SELECT GREATEST(reltuples::bigint, 0) FROM pg_class WHERE relname = 'query_cache'`).Scan(&size); err == nil {
		observability.QueryCacheSize.Set(float64(size))
	}
	return deleted, nil
}
