package ai

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// embeddingByteBufferPool recycles the byte slice used to pack float64
// embeddings as little-endian bytes. Pooled across SetMany calls because
// embedding-batch ingestion can run hundreds of marshalEmbedding calls
// back-to-back.
var embeddingByteBufferPool = sync.Pool{
	New: func() any { b := make([]byte, 0, 16384); return &b },
}

// embCacheKeyBufPool recycles the small byte slice that embCacheKey uses
// to concatenate (model + ":" + text) before hashing. Get / Set fire on
// every embedding cache lookup, so the per-call alloc adds up under search
// load. 4 KiB initial capacity covers typical "model_id:query" inputs;
// longer texts grow the buffer, and the pool keeps the grown capacity for
// subsequent reuse.
var embCacheKeyBufPool = sync.Pool{
	New: func() any { b := make([]byte, 0, 4096); return &b },
}

// EmbeddingCache is a Redis-backed cache for embedding vectors. When the
// underlying Redis client is nil the cache is a no-op (all lookups miss and
// stores are silently ignored), which lets callers pass a nil-safe instance
// without extra nil checks.
type EmbeddingCache struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewEmbeddingCache creates a new EmbeddingCache. If rdb is nil the cache is
// disabled (all operations are safe no-ops).
func NewEmbeddingCache(rdb *redis.Client, ttl time.Duration) *EmbeddingCache {
	return &EmbeddingCache{rdb: rdb, ttl: ttl}
}

// embCacheKey returns a deterministic Redis key for the given model+text pair.
// The key uses the "embb:" prefix (b for binary) so it cannot collide with
// any pre-existing JSON-encoded cache entries from older builds — those
// simply expire under their TTL while new code populates the binary cache.
//
// Uses sha256.Sum256 (stack-allocated state) instead of sha256.New() (which
// returns an interface and forces the hash state to escape to the heap).
// hex-encodes into a fixed-size stack array so the only remaining heap
// allocation is the final string copy (unavoidable since strings are
// immutable). This runs on every embedding cache lookup, so the per-call
// savings add up under search load.
func embCacheKey(model, text string) string {
	bufPtr := embCacheKeyBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	buf = append(buf, model...)
	buf = append(buf, ':')
	buf = append(buf, text...)
	sum := sha256.Sum256(buf)
	*bufPtr = buf
	embCacheKeyBufPool.Put(bufPtr)

	var keyBuf [5 + 64]byte // "embb:" (5) + hex(sha256) (64)
	copy(keyBuf[:5], "embb:")
	hex.Encode(keyBuf[5:], sum[:])
	return string(keyBuf[:])
}

// marshalEmbeddingInto writes a packed little-endian encoding of vec into
// dst, growing it as needed, and returns the (possibly reallocated) slice.
// Lets callers reuse a pooled buffer across many vectors. Output is exactly
// 8*len(vec) bytes — about 4-8x smaller and faster than JSON for typical
// 1536/2560/4096-dim embedding vectors.
func marshalEmbeddingInto(dst []byte, vec []float64) []byte {
	need := len(vec) * 8
	if cap(dst) < need {
		dst = make([]byte, need)
	} else {
		dst = dst[:need]
	}
	for i, f := range vec {
		binary.LittleEndian.PutUint64(dst[i*8:], math.Float64bits(f))
	}
	return dst
}

// unmarshalEmbedding decodes a packed little-endian float64 slice.
func unmarshalEmbedding(b []byte) ([]float64, bool) {
	if len(b) == 0 || len(b)%8 != 0 {
		return nil, false
	}
	vec := make([]float64, len(b)/8)
	for i := range vec {
		vec[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:]))
	}
	return vec, true
}

// Get retrieves a cached embedding vector. Returns (nil, false) on miss, error,
// or when the cache is disabled.
func (c *EmbeddingCache) Get(ctx context.Context, model, text string) ([]float64, bool) {
	if c == nil || c.rdb == nil {
		return nil, false
	}

	val, err := c.rdb.Get(ctx, embCacheKey(model, text)).Bytes()
	if err != nil {
		return nil, false
	}

	vec, ok := unmarshalEmbedding(val)
	if !ok {
		slog.Warn("embedding cache: malformed binary entry", "len", len(val))
		return nil, false
	}
	return vec, true
}

// Set stores an embedding vector in the cache. Errors are logged as warnings
// but never returned.
func (c *EmbeddingCache) Set(ctx context.Context, model, text string, vec []float64) {
	if c == nil || c.rdb == nil {
		return
	}

	// Single Set is synchronous: by the time c.rdb.Set returns, the byte
	// slice has been flushed to the connection and we own it again. Safe to
	// recycle through the pool.
	bufPtr := embeddingByteBufferPool.Get().(*[]byte)
	*bufPtr = marshalEmbeddingInto((*bufPtr)[:0], vec)
	if err := c.rdb.Set(ctx, embCacheKey(model, text), *bufPtr, c.ttl).Err(); err != nil {
		slog.Warn("embedding cache: set error", "error", err)
	}
	embeddingByteBufferPool.Put(bufPtr)
}

// SetMany pipelines multiple Set calls into a single Redis round-trip.
// Lengths of texts and vecs must match; mismatches are silently truncated to
// the shorter length. Errors are logged as warnings, never returned.
func (c *EmbeddingCache) SetMany(ctx context.Context, model string, texts []string, vecs [][]float64) {
	if c == nil || c.rdb == nil {
		return
	}
	n := len(texts)
	if len(vecs) < n {
		n = len(vecs)
	}
	if n == 0 {
		return
	}

	// Each vector needs its own []byte for the duration of pipe.Exec — the
	// pipeline buffers commands and only flushes them on Exec, so we cannot
	// reuse a single buffer across iterations. Allocate one big slab and
	// hand out subslices to amortize the per-vector allocation overhead.
	dim := 0
	for i := 0; i < n; i++ {
		if d := len(vecs[i]); d > dim {
			dim = d
		}
	}
	slab := make([]byte, n*dim*8)

	pipe := c.rdb.Pipeline()
	for i := 0; i < n; i++ {
		bytesLen := len(vecs[i]) * 8
		buf := slab[i*dim*8 : i*dim*8+bytesLen]
		for j, f := range vecs[i] {
			binary.LittleEndian.PutUint64(buf[j*8:], math.Float64bits(f))
		}
		pipe.Set(ctx, embCacheKey(model, texts[i]), buf, c.ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("embedding cache: pipelined set error", "error", err, "count", n)
	}
}
