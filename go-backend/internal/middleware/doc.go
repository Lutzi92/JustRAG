// Package middleware provides the HTTP middleware stack used by the
// application server. The stack is assembled in package app in this order
// (outer → inner): Recovery → CORS → SecurityHeaders → RequestID → OTel →
// Logging → Metrics → RateLimit → MaxBytes → handler.
//
// Highlights:
//
//   - RequestID generates an X-Request-Id when absent and propagates it via
//     context.Context so the logctx package can attach it to every log line.
//   - The wrappedWriter type captures status + bytes for Logging and
//     delegates http.Flusher so SSE handlers don't get buffered. The
//     rwUnwrapper interface lets handlers opt out of write deadlines.
//   - RateLimiter / RedisRateLimiter share a configurable category +
//     fail-open contract; a Redis outage during failure-counting now goes
//     through recordFailOpen so it surfaces in metrics.
package middleware
