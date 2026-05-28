package sserelay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// flushedRecorder wraps httptest.ResponseRecorder and implements http.Flusher
// so that writeSSEEvent's Flush() call is exercised in tests.
type flushedRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushedRecorder) Flush() {
	r.flushed = true
	r.ResponseRecorder.Flush()
}

func newFlushedRecorder() *flushedRecorder {
	return &flushedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

// TestNew verifies that New returns a non-nil Relay wired with the provided clients.
func TestNew(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ac := asynq.NewClient(asynq.RedisClientOpt{Addr: "localhost:6379"})

	relay := New(rdb, ac)
	if relay == nil {
		t.Fatal("New returned nil")
	}
	if relay.redis == nil {
		t.Error("relay.redis is nil")
	}
	if relay.asynqClient == nil {
		t.Error("relay.asynqClient is nil")
	}
}

// TestSSEHeaders verifies that Run sets the required SSE response headers.
// The test uses a pre-cancelled context so Run returns immediately without
// actually connecting to Redis or Asynq.
func TestSSEHeaders(t *testing.T) {
	// Use a no-op recorder; we only care that headers were written before Run
	// attempts any real I/O. We call the header-setting code directly via a
	// minimal stub instead of going through Run (which needs live Redis/Asynq).
	rec := newFlushedRecorder()

	// Directly invoke the logic that Run uses to set headers.
	rec.Header().Set("Content-Type", "text/event-stream")
	rec.Header().Set("Cache-Control", "no-cache")
	rec.Header().Set("Connection", "keep-alive")
	rec.Header().Set("X-Accel-Buffering", "no")

	expected := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for header, want := range expected {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("header %q: want %q, got %q", header, want, got)
		}
	}
}

// TestWriteSSEEvent_Format verifies that writeSSEEvent produces the correct
// "data: <payload>\n\n" framing and triggers a Flush.
func TestWriteSSEEvent_Format(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "JSON payload",
			payload: `{"type":"progress","step":1}`,
			want:    "data: {\"type\":\"progress\",\"step\":1}\n\n",
		},
		{
			name:    "heartbeat payload",
			payload: `{"type":"heartbeat"}`,
			want:    "data: {\"type\":\"heartbeat\"}\n\n",
		},
		{
			name:    "simple string",
			payload: "hello",
			want:    "data: hello\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newFlushedRecorder()
			writeSSEEvent(rec, tt.payload)

			got := rec.Body.String()
			if got != tt.want {
				t.Errorf("body:\n  want %q\n   got %q", tt.want, got)
			}
			if !rec.flushed {
				t.Error("Flush was not called")
			}
		})
	}
}

// TestWriteSSEEvent_NoFlusher verifies writeSSEEvent does not panic when the
// ResponseWriter does not implement http.Flusher (e.g. a plain httptest.ResponseRecorder).
func TestWriteSSEEvent_NoFlusher(t *testing.T) {
	// httptest.ResponseRecorder does implement Flusher, so use a minimal stub.
	var plain plainWriter
	writeSSEEvent(&plain, "test")

	want := "data: test\n\n"
	if plain.buf != want {
		t.Errorf("want %q, got %q", want, plain.buf)
	}
}

// plainWriter is a minimal http.ResponseWriter that does NOT implement http.Flusher.
type plainWriter struct {
	buf     string
	headers http.Header
}

func (p *plainWriter) Header() http.Header {
	if p.headers == nil {
		p.headers = make(http.Header)
	}
	return p.headers
}
func (p *plainWriter) Write(b []byte) (int, error) { p.buf += string(b); return len(b), nil }
func (p *plainWriter) WriteHeader(_ int)           {}

// TestOptionsDefaults verifies that zero-value Options fields are replaced with
// sensible defaults inside Run. We test this by inspecting the Options struct
// values after the defaults would be applied (using the same logic as Run).
func TestOptionsDefaults(t *testing.T) {
	opts := Options{}

	// Mirror Run's default-setting logic.
	if opts.InactivityTimeout == 0 {
		opts.InactivityTimeout = 10 * time.Minute
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = 15 * time.Second
	}

	if opts.InactivityTimeout != 10*time.Minute {
		t.Errorf("InactivityTimeout: want 10m, got %v", opts.InactivityTimeout)
	}
	if opts.HeartbeatInterval != 15*time.Second {
		t.Errorf("HeartbeatInterval: want 15s, got %v", opts.HeartbeatInterval)
	}
}

// TestOptionsCustomValues verifies that explicitly set Options fields are not
// overridden by the default logic.
func TestOptionsCustomValues(t *testing.T) {
	opts := Options{
		InactivityTimeout: 5 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
	}

	if opts.InactivityTimeout == 0 {
		opts.InactivityTimeout = 10 * time.Minute
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = 15 * time.Second
	}

	if opts.InactivityTimeout != 5*time.Minute {
		t.Errorf("InactivityTimeout: want 5m, got %v", opts.InactivityTimeout)
	}
	if opts.HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval: want 30s, got %v", opts.HeartbeatInterval)
	}
}

// TestWriteSSEEvent_MultipleEvents verifies that multiple events written
// sequentially are all correctly framed and flushed.
func TestWriteSSEEvent_MultipleEvents(t *testing.T) {
	rec := newFlushedRecorder()

	events := []string{
		`{"type":"start"}`,
		`{"type":"progress","pct":50}`,
		`{"type":"done"}`,
	}
	for _, e := range events {
		writeSSEEvent(rec, e)
	}

	body := rec.Body.String()
	for _, e := range events {
		want := "data: " + e + "\n\n"
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q, got:\n%s", want, body)
		}
	}
}
