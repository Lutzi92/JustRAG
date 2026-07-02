package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/splitter"
)

// ---------------------------------------------------------------------------
// ZeroVectorError
// ---------------------------------------------------------------------------

// ZeroVectorError is returned when an embedding model returns a zero vector.
// This is a deterministic failure and is never retried.
type ZeroVectorError struct {
	Model string
	Index int
}

func (e *ZeroVectorError) Error() string {
	return fmt.Sprintf("embedding model %q returned zero vector for text at index %d", e.Model, e.Index)
}

// DimensionMismatchError is returned when the provider's embedding size does
// not match the configured (requested) dimensions — i.e. the backend ignored
// or mishandled the `dimensions` parameter. Deterministic; never retried.
// Without this guard a backend that ignores the parameter would silently
// re-populate the wrong dim-keyed vector table.
type DimensionMismatchError struct {
	Model string
	Want  int
	Got   int
}

func (e *DimensionMismatchError) Error() string {
	return fmt.Sprintf("embedding model %q returned %d-dim vector but %d dimensions are configured (backend ignored the dimensions parameter?)", e.Model, e.Got, e.Want)
}

// ---------------------------------------------------------------------------
// Public functions
// ---------------------------------------------------------------------------

// EncodeQuery generates a query-side embedding, optionally wrapping the raw
// query in the Qwen3-Embedding instruction template:
//
//	Instruct: {instruction}
//	Query: {query}
//
// Qwen3-Embedding was trained with asymmetric query / document encoding:
// document embeddings stay bare (that's the trained behavior) while query
// embeddings get the instruction prefix. Passing the instruction at query time
// aligns the query vector with the trained search manifold. Instruction is
// typically configured at the KB level (site_configs key `query_instruction`);
// an empty instruction makes this function equivalent to GenerateEmbedding,
// so callers can pass it unconditionally.
//
// Note: the wrapped text is what lands in the embedding cache, so query-side
// and document-side encodings of the same string never collide.
//
// cache may be nil; EmbeddingCache.{Get,Set} are nil-safe.
func EncodeQuery(ctx context.Context, resolver *ConfigResolver, query, instruction, kbID string, cache *EmbeddingCache) ([]float64, error) {
	return GenerateEmbedding(ctx, resolver, BuildQueryInput(query, instruction), kbID, cache)
}

// BuildQueryInput returns the query text as it should be fed to the embedding
// model. When instruction is non-empty the Qwen3 "Instruct: … \nQuery: …"
// template is applied; otherwise the query is returned unchanged.
//
// strings.Builder with a pre-computed Grow avoids the three intermediate
// allocations that "a" + b + "c" + d emits on the hot query-embedding path.
func BuildQueryInput(query, instruction string) string {
	if instruction == "" {
		return query
	}
	const prefix = "Instruct: "
	const sep = "\nQuery: "
	var b strings.Builder
	b.Grow(len(prefix) + len(instruction) + len(sep) + len(query))
	b.WriteString(prefix)
	b.WriteString(instruction)
	b.WriteString(sep)
	b.WriteString(query)
	return b.String()
}

// GenerateEmbedding generates a single text embedding.
// The config is resolved for kbID (pass an empty string to use the global
// active provider). A zero vector returned by the model is rejected.
//
// Delegates to GenerateEmbeddings so it inherits the same retry/backoff
// policy: 3 attempts, 2s/4s/8s exponential backoff on transient errors.
// Cache lookup and store are handled by the batch path.
//
// cache may be nil; EmbeddingCache.{Get,Set} are nil-safe.
func GenerateEmbedding(ctx context.Context, resolver *ConfigResolver, text, kbID string, cache *EmbeddingCache) ([]float64, error) {
	embs, err := GenerateEmbeddings(ctx, resolver, []string{text}, kbID, cache)
	if err != nil {
		return nil, err
	}
	if len(embs) != 1 {
		return nil, fmt.Errorf("ai: GenerateEmbedding: expected 1 embedding, got %d", len(embs))
	}
	return embs[0], nil
}

// GenerateEmbeddings generates embeddings for multiple texts with retry logic.
// Max 3 attempts with exponential backoff (2s, 4s, 8s). A ZeroVectorError from
// any text is returned immediately without retry.
// The timeout for each attempt is 15s + 2s per text.
//
// cache may be nil; EmbeddingCache.{Get,SetMany} are nil-safe.
func GenerateEmbeddings(ctx context.Context, resolver *ConfigResolver, texts []string, kbID string, cache *EmbeddingCache) ([][]float64, error) {
	ctx, span := observability.Tracer().Start(ctx, "rag.embed")
	defer span.End()
	span.SetAttributes(attribute.Int("rag.embed.input_count", len(texts)))

	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	span.SetAttributes(attribute.String("rag.embed.model", cfg.EmbeddingModel))

	// Cache keys are scoped by requested dimensions: the same (model, text)
	// pair yields different vectors at different truncation sizes, and a
	// stale native-size entry served after an operator configures truncation
	// would poison the dim-keyed vector tables.
	cacheModel := embeddingCacheModel(cfg.EmbeddingModel, cfg.EmbeddingDimensions)

	// Check cache for each text — only send uncached texts to the API.
	// Pre-size the uncached slices to len(texts) so the cold-start path
	// (every text is uncached, e.g. fresh document ingestion) doesn't pay
	// geometric grow-and-copy on a 100-element batch.
	result := make([][]float64, len(texts))
	uncachedIndices := make([]int, 0, len(texts))
	var uncachedTexts []string

	if cache != nil {
		uncachedTexts = make([]string, 0, len(texts))
		// One pipelined MGET for the whole batch instead of len(texts) serial
		// GETs — the cold-start ingestion path (a 100-text batch, every text a
		// miss) previously paid 100 round-trips just to discover they all miss.
		cached := cache.GetMany(ctx, cacheModel, texts)
		for i := range texts {
			if cached[i] != nil {
				observability.RecordEmbeddingCacheHit()
				result[i] = cached[i]
			} else {
				observability.RecordEmbeddingCacheMiss()
				uncachedIndices = append(uncachedIndices, i)
				uncachedTexts = append(uncachedTexts, texts[i])
			}
		}
	} else {
		for i := range texts {
			uncachedIndices = append(uncachedIndices, i)
		}
		uncachedTexts = texts
	}

	// All cached — return immediately.
	if len(uncachedTexts) == 0 {
		return result, nil
	}

	const maxAttempts = 3
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	timeout := 15*time.Second + time.Duration(len(uncachedTexts))*2*time.Second

	// Construct the HTTP client once so its connection pool is reused across
	// retries. A fresh *http.Transport per attempt would defeat TCP keep-alive
	// on the failing-then-recovering path (same reason completion.go hoists it).
	// CachedClient additionally memoises the Client wrapper across batch calls
	// — the underlying *http.Transport is the package-level sharedTransport,
	// so connection-pool reuse is the same either way.
	client := CachedClient(cfg.BaseURL, cfg.APIKey)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		embeddings, err := doEmbeddingBatch(ctx, client, cfg.EmbeddingModel, cfg.EmbeddingDimensions, uncachedTexts, timeout)
		if err == nil {
			// Place API results into the correct positions.
			for j, idx := range uncachedIndices {
				result[idx] = embeddings[j]
			}

			// Store new embeddings in cache (fire-and-forget). One goroutine
			// pipelines the whole batch into a single Redis round-trip rather
			// than spawning N goroutines for N writes — under concurrent
			// ingestion (multiple enrichment workers × ~20-text batches)
			// the per-write goroutine pattern produced spikes of hundreds of
			// live goroutines competing for the connection pool.
			if cache != nil {
				bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				batchTexts := make([]string, len(uncachedIndices))
				batchVecs := make([][]float64, len(uncachedIndices))
				for j, idx := range uncachedIndices {
					batchTexts[j] = texts[idx]
					batchVecs[j] = embeddings[j]
				}
				safego.GoCtx(ctx, func() {
					defer bgCancel()
					cache.SetMany(bgCtx, cacheModel, batchTexts, batchVecs)
				})
			}

			return result, nil
		}

		// Deterministic failures — never retry.
		if isDeterministicEmbeddingError(err) {
			return nil, err
		}

		lastErr = err

		// Don't sleep after the final attempt.
		if attempt < maxAttempts-1 {
			t := time.NewTimer(backoff[attempt])
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
	}

	return nil, fmt.Errorf("ai: embedding failed after %d attempts: %w", maxAttempts, lastErr)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// doEmbeddingBatch performs a single batch embedding call with the given timeout.
// The caller supplies the client so its connection pool is reused across retries.
// dimensions > 0 requests MRL-truncated vectors of that size and rejects
// responses of any other length (a backend silently ignoring the parameter
// must not re-populate the wrong dim-keyed vector table).
func doEmbeddingBatch(ctx context.Context, client *Client, model string, dimensions int, texts []string, timeout time.Duration) ([][]float64, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := client.Embedding(callCtx, &EmbeddingRequest{
		Model:      model,
		Input:      texts,
		Dimensions: dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("ai: embedding request: %w", err)
	}

	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("ai: expected %d embeddings, got %d", len(texts), len(resp.Data))
	}

	// Data is already sorted by Index (client.go guarantees this).
	result := make([][]float64, len(texts))
	for _, d := range resp.Data {
		if isZeroVector(d.Embedding) {
			return nil, &ZeroVectorError{Model: model, Index: d.Index}
		}
		if dimensions > 0 && len(d.Embedding) != dimensions {
			return nil, &DimensionMismatchError{Model: model, Want: dimensions, Got: len(d.Embedding)}
		}
		result[d.Index] = d.Embedding
	}

	return result, nil
}

// embeddingCacheModel returns the model identifier used for embedding-cache
// keys. dims > 0 appends a suffix so truncated and native vectors of the
// same model never share a key; dims == 0 keeps the historical bare-model
// key, so existing cache entries stay valid for unconfigured deployments.
func embeddingCacheModel(model string, dims int) string {
	if dims <= 0 {
		return model
	}
	return fmt.Sprintf("%s#dim=%d", model, dims)
}

// isZeroVector returns true when every element of v is 0 (or v is empty).
func isZeroVector(v []float64) bool {
	for _, f := range v {
		if f != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Late chunking
// ---------------------------------------------------------------------------

// GenerateEmbeddingsLateChunked embeds an ordered slice of chunks of ONE
// document using Jina-style late chunking: the provider concatenates the
// inputs, encodes the concatenation with a long-context embedder once, then
// returns one mean-pooled vector per element whose values reflect the full
// document context.
//
// chunks must be in source order (contiguous run of the document) for the
// pooled vectors to carry meaningful cross-chunk context.
//
// maxInputTokens caps the per-call token budget (tiktoken cl100k_base
// estimate, same heuristic the splitter uses for chunk sizing). Chunks that
// don't fit are packed into successive windows; a single chunk larger than
// the budget is sent alone (still pooled by the server).
//
// The embedding cache is intentionally bypassed: late-chunked vectors depend
// on document context, so the GenerateEmbeddings (model, text) cache key
// would conflate different files. Re-ingestion is cold by design here.
//
// Retry / backoff mirrors GenerateEmbeddings (3 attempts, 2s/4s/8s).
func GenerateEmbeddingsLateChunked(ctx context.Context, resolver *ConfigResolver, chunks []string, kbID string, maxInputTokens int) ([][]float64, error) {
	ctx, span := observability.Tracer().Start(ctx, "rag.embed.late_chunking")
	defer span.End()
	span.SetAttributes(attribute.Int("rag.embed.input_count", len(chunks)))

	if len(chunks) == 0 {
		return nil, nil
	}
	if maxInputTokens <= 0 {
		maxInputTokens = 8192
	}

	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	span.SetAttributes(attribute.String("rag.embed.model", cfg.EmbeddingModel))

	windows := packLateChunkingWindows(chunks, maxInputTokens)
	span.SetAttributes(attribute.Int("rag.embed.windows", len(windows)))

	client := CachedClient(cfg.BaseURL, cfg.APIKey)

	result := make([][]float64, len(chunks))
	for _, w := range windows {
		windowTexts := chunks[w.start:w.end]
		timeout := 30*time.Second + time.Duration(len(windowTexts))*2*time.Second
		vecs, err := doLateChunkedBatchWithRetry(ctx, client, cfg.EmbeddingModel, cfg.EmbeddingDimensions, windowTexts, timeout)
		if err != nil {
			return nil, err
		}
		copy(result[w.start:w.end], vecs)
	}
	return result, nil
}

type lateChunkingWindow struct {
	start int // inclusive
	end   int // exclusive
}

// packLateChunkingWindows groups consecutive chunks into windows whose total
// token count (tiktoken cl100k_base estimate) does not exceed maxTokens.
// Chunks larger than maxTokens occupy a single-element window of their own.
// Order is preserved across windows.
func packLateChunkingWindows(chunks []string, maxTokens int) []lateChunkingWindow {
	if len(chunks) == 0 {
		return nil
	}
	windows := make([]lateChunkingWindow, 0, 4)
	start := 0
	used := 0
	for i, c := range chunks {
		tok := splitter.CountTokens(c)
		// Oversized single chunk: flush any pending window first, then
		// emit this chunk as its own window. Avoids silently dropping
		// content when one chunk exceeds the per-call budget.
		if tok > maxTokens {
			if start < i {
				windows = append(windows, lateChunkingWindow{start: start, end: i})
			}
			windows = append(windows, lateChunkingWindow{start: i, end: i + 1})
			start = i + 1
			used = 0
			continue
		}
		if used+tok > maxTokens && start < i {
			windows = append(windows, lateChunkingWindow{start: start, end: i})
			start = i
			used = 0
		}
		used += tok
	}
	if start < len(chunks) {
		windows = append(windows, lateChunkingWindow{start: start, end: len(chunks)})
	}
	return windows
}

func doLateChunkedBatchWithRetry(ctx context.Context, client *Client, model string, dimensions int, texts []string, timeout time.Duration) ([][]float64, error) {
	const maxAttempts = 3
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := client.Embedding(callCtx, &EmbeddingRequest{
			Model:        model,
			Input:        texts,
			LateChunking: true,
			Dimensions:   dimensions,
		})
		cancel()
		if err == nil {
			if len(resp.Data) != len(texts) {
				return nil, fmt.Errorf("ai: late-chunked embedding returned %d vectors for %d inputs", len(resp.Data), len(texts))
			}
			result := make([][]float64, len(texts))
			for _, d := range resp.Data {
				if isZeroVector(d.Embedding) {
					return nil, &ZeroVectorError{Model: model, Index: d.Index}
				}
				if dimensions > 0 && len(d.Embedding) != dimensions {
					return nil, &DimensionMismatchError{Model: model, Want: dimensions, Got: len(d.Embedding)}
				}
				result[d.Index] = d.Embedding
			}
			return result, nil
		}

		if isDeterministicEmbeddingError(err) {
			return nil, err
		}

		lastErr = err
		if attempt < maxAttempts-1 {
			t := time.NewTimer(backoff[attempt])
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
	}
	return nil, fmt.Errorf("ai: late-chunked embedding failed after %d attempts: %w", maxAttempts, lastErr)
}

// isDeterministicEmbeddingError reports whether err is guaranteed to recur on
// retry: zero-vector responses, dimension mismatches, and client-side API
// errors (4xx, e.g. vLLM rejecting the `dimensions` parameter for a model
// without declared Matryoshka support). 408 (timeout) and 429 (rate limit)
// are transient and stay retryable.
func isDeterministicEmbeddingError(err error) bool {
	var zve *ZeroVectorError
	var dme *DimensionMismatchError
	if errors.As(err, &zve) || errors.As(err, &dme) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		s := apiErr.StatusCode
		return s >= 400 && s < 500 && s != 408 && s != 429
	}
	return false
}
