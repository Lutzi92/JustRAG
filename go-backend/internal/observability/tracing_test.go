package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installTestExporter swaps the global tracer provider with one that exports
// to an in-memory recorder. Returns the recorder and a cleanup.
func installTestExporter(t *testing.T) (*tracetest.InMemoryExporter, func()) {
	t.Helper()
	prev := otel.GetTracerProvider()
	rec := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(rec))
	otel.SetTracerProvider(tp)
	return rec, func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}
}

func TestInitTracing_NoEndpointReturnsNoopShutdown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown, err := InitTracing(context.Background(), "justrag-test", "0.0.0")
	if err != nil {
		t.Fatalf("expected no error when endpoint empty, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must never be nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("noop shutdown returned error: %v", err)
	}
}

func TestStartGenerationSpan_TagsObservationType(t *testing.T) {
	rec, cleanup := installTestExporter(t)
	defer cleanup()

	_, span := StartGenerationSpan(context.Background(), "rag.test_gen", GenerationAttrs{
		System: "openai",
		Model:  "gpt-4o",
	})
	span.End()

	spans := rec.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	got := spans[0]
	if got.Name != "rag.test_gen" {
		t.Errorf("expected span name rag.test_gen, got %q", got.Name)
	}

	attrs := map[string]string{}
	for _, kv := range got.Attributes {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	for k, want := range map[string]string{
		"langfuse.observation.type": "generation",
		"gen_ai.system":             "openai",
		"gen_ai.request.model":      "gpt-4o",
	} {
		if attrs[k] != want {
			t.Errorf("attr %s = %q, want %q (all attrs: %+v)", k, attrs[k], want, attrs)
		}
	}
}

func TestStartGenerationSpan_OmitsZeroTokens(t *testing.T) {
	rec, cleanup := installTestExporter(t)
	defer cleanup()

	_, span := StartGenerationSpan(context.Background(), "rag.test_gen", GenerationAttrs{
		System: "openai",
		Model:  "gpt-4o",
	})
	span.End()

	for _, kv := range rec.GetSpans()[0].Attributes {
		if string(kv.Key) == "gen_ai.usage.input_tokens" || string(kv.Key) == "gen_ai.usage.output_tokens" {
			t.Errorf("expected no usage attrs when zero, got %s=%v", kv.Key, kv.Value)
		}
	}
}

func TestStartGenerationSpan_EmptySystemBecomesUnknown(t *testing.T) {
	rec, cleanup := installTestExporter(t)
	defer cleanup()

	_, span := StartGenerationSpan(context.Background(), "rag.test_gen", GenerationAttrs{Model: "x"})
	span.End()

	for _, kv := range rec.GetSpans()[0].Attributes {
		if string(kv.Key) == "gen_ai.system" {
			if kv.Value.Emit() != "unknown" {
				t.Errorf("expected unknown, got %v", kv.Value)
			}
			return
		}
	}
	t.Error("gen_ai.system attribute not found")
}

func TestTracer_NeverNil(t *testing.T) {
	if Tracer() == nil {
		t.Fatal("Tracer should never return nil")
	}
}

func TestProviderSystemFromBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com/v1/":      "openai",
		"https://api.anthropic.com/v1/":   "anthropic",
		"https://api.deepseek.com/v1/":    "deepseek",
		"https://api.cohere.ai/":          "cohere",
		"https://my-custom-llm.local/v1/": "unknown",
		"":                                "unknown",
	}
	for url, want := range cases {
		if got := ProviderSystemFromBaseURL(url); got != want {
			t.Errorf("URL %q: expected %q, got %q", url, want, got)
		}
	}
}
