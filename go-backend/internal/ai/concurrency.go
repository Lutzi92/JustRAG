package ai

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"
)

// maxConcurrentRequests is the process-wide cap on simultaneous in-flight
// unary requests to a single model-provider endpoint, read once from the
// AI_MAX_CONCURRENT_REQUESTS env var. Zero (the default) disables the cap and
// preserves the prior unbounded behaviour exactly.
//
// Why this exists: every fan-out site in the app bounds its OWN concurrency
// (enrichment, KG extraction, multi-query, rerank, RAPTOR, …), but nothing
// bounds the PRODUCT. Under an ingest burst, N worker tasks each running M
// enrichment calls hit the same vLLM/embedding backend simultaneously — plus
// chat traffic on the same endpoint. The provider tends to absorb that as
// timeouts rather than graceful slowdown. This optional cap turns provider
// saturation into backpressure: callers block on Acquire instead of piling
// onto the backend. It composes UNDER the per-op semaphores; set it to the
// backend's safe concurrent-request ceiling.
//
// Scope — unary calls only. The cap gates the doJSON path (ChatCompletion,
// Embedding, Rerank, ListModels) and deliberately NOT the streaming path
// (StreamChatCompletion, which bypasses doJSON). A streamed answer holds its
// connection open for many seconds; gating it here would let long-lived
// streams starve the short unary calls (embeddings, reranks) that answer-time
// tools depend on. Because doJSON acquires a single slot on entry and releases
// it on return — never calling another doJSON while holding one — no goroutine
// ever waits on a second slot while holding the first, so the cap is
// deadlock-free by construction.
var maxConcurrentRequests = sync.OnceValue(func() int64 {
	v := strings.TrimSpace(os.Getenv("AI_MAX_CONCURRENT_REQUESTS"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
})

// providerSems memoises one weighted semaphore per base URL so that distinct
// endpoints (e.g. a chat deployment vs. a separate embedding deployment) get
// independent caps rather than sharing a single global budget.
var providerSems sync.Map // baseURL -> *semaphore.Weighted

// acquireProviderSlot blocks until a slot is free for baseURL, returning a
// release func the caller must invoke (deferred). When the cap is disabled it
// returns a no-op release and never blocks.
func acquireProviderSlot(ctx context.Context, baseURL string) (func(), error) {
	limit := maxConcurrentRequests()
	if limit <= 0 {
		return func() {}, nil
	}
	v, ok := providerSems.Load(baseURL)
	if !ok {
		v, _ = providerSems.LoadOrStore(baseURL, semaphore.NewWeighted(limit))
	}
	sem := v.(*semaphore.Weighted)
	if err := sem.Acquire(ctx, 1); err != nil {
		// ctx cancelled/expired while queued — surface it; the caller's
		// request would have failed on the same ctx anyway.
		return nil, fmt.Errorf("ai: wait for provider slot: %w", err)
	}
	return func() { sem.Release(1) }, nil
}
