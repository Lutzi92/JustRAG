package logctx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/requestid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newJSONLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func parseLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("invalid json: %v\nline: %s", err, line)
	}
	return m
}

func TestFrom_NoContextReturnsBaseLogger(t *testing.T) {
	var buf bytes.Buffer
	SetBase(newJSONLogger(&buf))
	t.Cleanup(func() { SetBase(nil) })

	From(context.Background()).Info("hello")
	got := parseLine(t, strings.TrimSpace(buf.String()))
	if got["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", got["msg"])
	}
	if _, has := got["request_id"]; has {
		t.Fatalf("expected no request_id when absent, got %v", got["request_id"])
	}
}

func TestFrom_AttachesRequestID(t *testing.T) {
	var buf bytes.Buffer
	SetBase(newJSONLogger(&buf))
	t.Cleanup(func() { SetBase(nil) })

	ctx := requestid.NewContext(context.Background(), "abc123")
	From(ctx).Info("hello")

	got := parseLine(t, strings.TrimSpace(buf.String()))
	if got["request_id"] != "abc123" {
		t.Fatalf("expected request_id=abc123, got %v", got["request_id"])
	}
}

func TestWithKBAndUser_AttachAllFields(t *testing.T) {
	var buf bytes.Buffer
	SetBase(newJSONLogger(&buf))
	t.Cleanup(func() { SetBase(nil) })

	ctx := requestid.NewContext(context.Background(), "rid")
	ctx = WithUser(ctx, "user-1")
	ctx = WithKB(ctx, "kb-1")
	From(ctx).Info("hello")

	got := parseLine(t, strings.TrimSpace(buf.String()))
	for k, want := range map[string]any{
		"request_id": "rid",
		"user_id":    "user-1",
		"kb_id":      "kb-1",
	} {
		if got[k] != want {
			t.Errorf("expected %s=%v, got %v", k, want, got[k])
		}
	}
}

func TestSetBase_NilFallsBackToDefault(t *testing.T) {
	SetBase(nil)
	if From(context.Background()) == nil {
		t.Fatal("From should never return nil")
	}
}

func TestWithUser_EmptyIsNoop(t *testing.T) {
	ctx := WithUser(context.Background(), "")
	var buf bytes.Buffer
	SetBase(newJSONLogger(&buf))
	t.Cleanup(func() { SetBase(nil) })

	From(ctx).Info("hello")
	got := parseLine(t, strings.TrimSpace(buf.String()))
	if _, has := got["user_id"]; has {
		t.Fatalf("expected no user_id for empty input, got %v", got["user_id"])
	}
}

func TestFrom_AttachesTraceIDWhenSpanActive(t *testing.T) {
	rec := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})

	var buf bytes.Buffer
	SetBase(newJSONLogger(&buf))
	t.Cleanup(func() { SetBase(nil) })

	ctx, span := otel.Tracer("test").Start(context.Background(), "outer")
	defer span.End()

	From(ctx).Info("hello")

	got := parseLine(t, strings.TrimSpace(buf.String()))
	if got["trace_id"] == nil || got["trace_id"] == "" {
		t.Errorf("expected trace_id present, got %+v", got)
	}
	if got["span_id"] == nil || got["span_id"] == "" {
		t.Errorf("expected span_id present, got %+v", got)
	}
}
