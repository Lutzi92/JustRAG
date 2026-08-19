package observability

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/justrag/go-backend"

// ShutdownFunc shuts down the tracer provider, flushing any pending spans.
// Always non-nil; safe to call even when tracing is disabled.
type ShutdownFunc func(context.Context) error

// InitTracing configures the global OpenTelemetry tracer provider.
//
// When OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is unset, this returns a no-op
// ShutdownFunc and DOES NOT install a tracer provider — the global provider
// remains the SDK default no-op, so all tracer.Start calls are essentially
// free.
//
// When the endpoint is set, an OTLP/HTTP exporter is wired up with batched
// span processing. Standard OTel env vars apply (OTEL_SERVICE_NAME,
// OTEL_EXPORTER_OTLP_TRACES_HEADERS, OTEL_RESOURCE_ATTRIBUTES, etc.).
//
// serviceName is used as the default if OTEL_SERVICE_NAME is unset.
//
// redactPath, when non-nil, installs a span processor that rewrites the
// url.path attribute otelhttp records from the raw request — see
// pathRedactingProcessor for why the span-name formatter is not enough.
// Callers without an HTTP surface (the worker) pass nil.
func InitTracing(ctx context.Context, serviceName, version string, redactPath PathRedactor) (ShutdownFunc, error) {
	// Honor both the signal-specific and the general OTLP endpoint env vars.
	// The OTel SDK respects either; we only short-circuit when both are empty.
	if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		_ = os.Setenv("OTEL_SERVICE_NAME", serviceName)
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcessRuntimeDescription(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	}
	// Registered ahead of the batcher so the rewrite happens at span start,
	// long before anything is exported.
	if redactPath != nil {
		opts = append(opts, sdktrace.WithSpanProcessor(pathRedactingProcessor{redact: redactPath}))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Tracer returns the package-wide tracer. Safe to call before InitTracing —
// it returns the no-op tracer until init runs.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// GenerationAttrs is the minimum metadata Langfuse needs to render a span as
// an LLM generation observation. Token counts are optional — set to zero to
// omit (we do not fabricate values we don't have).
type GenerationAttrs struct {
	System       string // e.g. "openai", "anthropic"; "unknown" if not derivable
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// StartGenerationSpan starts a span tagged for Langfuse "generation" rendering.
// Caller MUST End() the returned span (typical pattern: defer span.End()).
func StartGenerationSpan(ctx context.Context, name string, gen GenerationAttrs) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, name)
	system := gen.System
	if system == "" {
		system = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("langfuse.observation.type", "generation"),
		attribute.String("gen_ai.system", system),
	}
	if gen.Model != "" {
		attrs = append(attrs, attribute.String("gen_ai.request.model", gen.Model))
	}
	if gen.InputTokens > 0 {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.input_tokens", gen.InputTokens))
	}
	if gen.OutputTokens > 0 {
		attrs = append(attrs, attribute.Int64("gen_ai.usage.output_tokens", gen.OutputTokens))
	}
	span.SetAttributes(attrs...)
	return ctx, span
}

// ProviderSystemFromBaseURL maps an AI provider base URL to the gen_ai.system
// value Langfuse / OTel semantic conventions expect.
func ProviderSystemFromBaseURL(baseURL string) string {
	switch {
	case strings.Contains(baseURL, "openai.com"):
		return "openai"
	case strings.Contains(baseURL, "anthropic.com"):
		return "anthropic"
	case strings.Contains(baseURL, "deepseek.com"):
		return "deepseek"
	case strings.Contains(baseURL, "cohere.ai"):
		return "cohere"
	default:
		return "unknown"
	}
}
