package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/justrag/go-backend/internal/middleware"
	"github.com/justrag/go-backend/internal/observability"
)

// The invite token is the only secret this application carries in a URL path,
// and it is a permanent credential granting up to admin on a KB. Redacting the
// span NAME is not enough: otelhttp records url.path as a span ATTRIBUTE built
// from the raw request, independently of WithSpanNameFormatter.
//
// internal/observability covers the processor against a synthetic span. This
// test covers the assembly instead — the real otelhttp middleware, the real
// middleware.RedactSecretPath, and the real span processor, wired the way
// server.go wires them — so it also fails if otelhttp ever stops attaching
// url.path at span start (which is what makes an OnStart processor able to see
// it at all), or if the SDK stops letting a later value win.
func TestOTelSpanAttributesCarryNoInviteToken(t *testing.T) {
	const token = "AbCdEf0123456789AbCdEf0123456789AbCdEf012"

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(observability.NewPathRedactingProcessor(middleware.RedactSecretPath)),
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	// Mirrors server.go's otelhttp.NewMiddleware wiring, but against this
	// provider rather than the global one.
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler = otelhttp.NewMiddleware("justrag-test",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + middleware.NormalizeRoute(r.URL.Path)
		}),
	)(handler)

	for _, path := range []string{
		"/api/invites/" + token + "/redeem",
		"/join/" + token, // the SPA shell request carries the same secret
	} {
		exp.Reset()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)

		spans := exp.GetSpans()
		if len(spans) != 1 {
			t.Fatalf("%s: got %d spans, want 1", path, len(spans))
		}
		if name := spans[0].Name; strings.Contains(name, token) {
			t.Errorf("%s: span name %q contains the token", path, name)
		}
		for _, a := range spans[0].Attributes {
			if strings.Contains(a.Value.String(), token) {
				t.Errorf("%s: span attribute %s = %q contains the token", path, a.Key, a.Value.String())
			}
		}
	}
}

// The redaction must not flatten unrelated routes, or every other span's
// url.path would be lost.
func TestOTelSpanAttributesKeepOrdinaryPaths(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(observability.NewPathRedactingProcessor(middleware.RedactSecretPath)),
		sdktrace.WithSyncer(exp),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	handler = otelhttp.NewMiddleware("justrag-test", otelhttp.WithTracerProvider(tp))(handler)

	const path = "/api/kb/3fa85f64-5717-4562-b3fc-2c963f66afa6/chat"
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	var got string
	for _, a := range spans[0].Attributes {
		if a.Key == "url.path" {
			got = a.Value.AsString()
		}
	}
	if got != path {
		t.Fatalf("url.path = %q, want it unchanged (%q)", got, path)
	}
}
