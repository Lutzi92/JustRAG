package observability

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// PathRedactor rewrites a URL path so a secret carried in it never leaves the
// process. internal/middleware.RedactSecretPath is the implementation used by
// the server; the parameter exists so this package does not import middleware
// (which imports this one for its metrics counters).
type PathRedactor func(string) string

// pathRedactingProcessor overwrites the url.path attribute on every span with
// its redacted form.
//
// It exists because otelhttp builds url.path from the raw request
// (otelhttp/internal/semconv/server.go sets semconv.URLPath(req.URL.Path))
// entirely independently of WithSpanNameFormatter. Redacting the span NAME —
// which app/server.go already does via middleware.NormalizeRoute — therefore
// leaves the secret sitting in the span's attributes, where it reaches the
// trace backend verbatim. The invite-link token in
// POST /api/invites/{token}/redeem is the only such secret today; it is a
// permanent credential granting up to admin on a knowledge base, so it must
// not be stored in a system whose read access is broader than the database's.
//
// Rewriting rather than dropping keeps the attribute useful: an operator still
// sees which route was hit, just with the secret segment replaced.
//
// The mechanism relies on a documented SDK behaviour: SetAttributes appends
// without deduplicating, and the deduplication that runs when attributes are
// read keeps the LAST occurrence of a key (recordingSpan.dedupeAttrsFromRecord
// assigns unique[idx] = a). Appending here therefore overrides the value
// otelhttp recorded at span creation. TestPathRedactingProcessor pins that
// end to end through a real exporter, so an SDK change that reversed the
// precedence would fail rather than silently start leaking.
type pathRedactingProcessor struct {
	redact PathRedactor
}

// NewPathRedactingProcessor returns the processor InitTracing installs. It is
// exported so internal/app can assert the real assembly — otelhttp, the real
// redactor and this processor together — rather than only the unit behaviour.
func NewPathRedactingProcessor(redact PathRedactor) sdktrace.SpanProcessor {
	return pathRedactingProcessor{redact: redact}
}

// OnStart rewrites url.path if the redactor changes it.
func (p pathRedactingProcessor) OnStart(_ context.Context, s sdktrace.ReadWriteSpan) {
	for _, attr := range s.Attributes() {
		if attr.Key != semconv.URLPathKey {
			continue
		}
		raw := attr.Value.AsString()
		if red := p.redact(raw); red != raw {
			s.SetAttributes(semconv.URLPath(red))
		}
		return
	}
}

// OnEnd, Shutdown and ForceFlush are no-ops: this processor only mutates
// attributes at span start and exports nothing itself.
func (pathRedactingProcessor) OnEnd(sdktrace.ReadOnlySpan)      {}
func (pathRedactingProcessor) Shutdown(context.Context) error   { return nil }
func (pathRedactingProcessor) ForceFlush(context.Context) error { return nil }
