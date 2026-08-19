package observability

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// fakeRedact stands in for middleware.RedactSecretPath (which this package
// cannot import — middleware imports observability). Same shape: collapse the
// token segment of an invite path, leave everything else alone.
func fakeRedact(path string) string {
	const prefix, suffix = "/api/invites/", "/redeem"
	if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix) {
		return prefix + "{token}" + suffix
	}
	return path
}

// newRecorder builds a provider wired exactly like InitTracing does — the
// redacting processor registered ahead of the exporting one — and returns the
// tracer plus the exporter that captures what would go over the wire.
func newRecorder(t *testing.T) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(pathRedactingProcessor{redact: fakeRedact}),
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exp, tp
}

// urlPath returns the exported span's url.path attribute value.
func urlPath(t *testing.T, spans tracetest.SpanStubs) string {
	t.Helper()
	if len(spans) != 1 {
		t.Fatalf("got %d exported spans, want 1", len(spans))
	}
	var found []string
	for _, a := range spans[0].Attributes {
		if a.Key == semconv.URLPathKey {
			found = append(found, a.Value.AsString())
		}
	}
	if len(found) != 1 {
		// More than one means the SDK stopped deduplicating on read and BOTH
		// the raw and the redacted value would ship — the exact silent-leak
		// mode this processor depends on not happening.
		t.Fatalf("got %d url.path attributes %v, want exactly 1", len(found), found)
	}
	return found[0]
}

// The token must not survive into an exported span. This asserts on what the
// exporter actually receives, not on the processor's internals — the leak this
// guards against is a value reaching the trace backend.
func TestPathRedactingProcessor_RedactsInviteToken(t *testing.T) {
	const token = "AbCdEf0123456789AbCdEf0123456789AbCdEf012"
	exp, tp := newRecorder(t)

	_, span := tp.Tracer("test").Start(context.Background(), "POST /api/invites/{token}/redeem",
		trace.WithAttributes(semconv.URLPath("/api/invites/"+token+"/redeem")))
	span.End()

	got := urlPath(t, exp.GetSpans())
	if strings.Contains(got, token) {
		t.Fatalf("exported url.path %q still contains the token", got)
	}
	if got != "/api/invites/{token}/redeem" {
		t.Fatalf("exported url.path = %q, want the redacted form", got)
	}
}

// Ordinary paths must be untouched, or every other span's url.path would be
// mangled by this processor.
func TestPathRedactingProcessor_LeavesOtherPathsAlone(t *testing.T) {
	exp, tp := newRecorder(t)
	const path = "/api/kb/3fa85f64-5717-4562-b3fc-2c963f66afa6/chat"

	_, span := tp.Tracer("test").Start(context.Background(), "POST "+path,
		trace.WithAttributes(semconv.URLPath(path)))
	span.End()

	if got := urlPath(t, exp.GetSpans()); got != path {
		t.Fatalf("exported url.path = %q, want it unchanged (%q)", got, path)
	}
}

// A span with no url.path at all (every non-HTTP span in the app) must pass
// through without the processor inventing one.
func TestPathRedactingProcessor_IgnoresSpansWithoutPath(t *testing.T) {
	exp, tp := newRecorder(t)

	_, span := tp.Tracer("test").Start(context.Background(), "rag.embed")
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	for _, a := range spans[0].Attributes {
		if a.Key == semconv.URLPathKey {
			t.Fatalf("processor added a url.path attribute (%q) to a span that had none", a.Value.AsString())
		}
	}
}
