package safego

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// signalHandler is a slog.Handler that pushes every record onto a channel so
// tests can deterministically wait for the recovery log without racing against
// the goroutine's exit.
type signalHandler struct {
	mu      sync.Mutex
	records chan slog.Record
}

func newSignalHandler() *signalHandler {
	return &signalHandler{records: make(chan slog.Record, 16)}
}

func (h *signalHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *signalHandler) Handle(_ context.Context, r slog.Record) error {
	// Clone to avoid races: slog reuses the record.
	clone := r.Clone()
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case h.records <- clone:
	default:
	}
	return nil
}

func (h *signalHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *signalHandler) WithGroup(string) slog.Handler      { return h }

func (h *signalHandler) wait(t *testing.T) slog.Record {
	t.Helper()
	select {
	case r := <-h.records:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slog record")
		return slog.Record{}
	}
}

// recordText returns the slog record's message and attribute values flattened
// into a single string for substring assertions.
func recordText(r slog.Record) string {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	return b.String()
}

// installSignalHandler swaps the default slog logger to a signalHandler for
// the test's lifetime, restoring the previous default afterwards.
func installSignalHandler(t *testing.T) *signalHandler {
	t.Helper()
	h := newSignalHandler()
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestGo_RunsFunction(t *testing.T) {
	done := make(chan struct{})
	Go(func() {
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fn to run")
	}
}

func TestGo_RecoversPanic(t *testing.T) {
	h := installSignalHandler(t)
	Go(func() {
		panic("boom")
	})
	r := h.wait(t)
	text := recordText(r)
	if !strings.Contains(text, "goroutine panic recovered") {
		t.Errorf("expected recovery log, got: %s", text)
	}
	if !strings.Contains(text, "boom") {
		t.Errorf("expected panic value 'boom' in log, got: %s", text)
	}
	if !strings.Contains(text, "stack=") {
		t.Errorf("expected stack trace in log, got: %s", text)
	}
}

func TestRecoverError_NoPanic(t *testing.T) {
	var err error
	func() {
		defer RecoverError(&err)
		// no panic
	}()
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestRecoverError_CapturesPanicAsError(t *testing.T) {
	installSignalHandler(t)
	var err error
	func() {
		defer RecoverError(&err)
		panic("kaboom")
	}()
	if err == nil {
		t.Fatal("expected error to be set")
	}
	if !strings.Contains(err.Error(), "panic: kaboom") {
		t.Errorf("expected error text to contain panic value, got %q", err.Error())
	}
}

func TestRecoverError_CapturesErrorPanic(t *testing.T) {
	installSignalHandler(t)
	sentinel := errors.New("sentinel")
	var err error
	func() {
		defer RecoverError(&err)
		panic(sentinel)
	}()
	if err == nil {
		t.Fatal("expected error to be set")
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Errorf("expected sentinel text in error, got %q", err.Error())
	}
}

func TestRecoverError_NilPointerLogs(t *testing.T) {
	h := installSignalHandler(t)
	// When errp is nil there's nowhere to surface the panic, so RecoverError
	// falls back to logging so the process still notices. This is misuse —
	// the log message says so — but it's a safer fallback than swallowing.
	func() {
		defer RecoverError(nil)
		panic("ignored")
	}()
	r := h.wait(t)
	text := recordText(r)
	if !strings.Contains(text, "goroutine panic recovered with nil errp") {
		t.Errorf("expected nil-errp warning log, got: %s", text)
	}
}

func TestRecoverError_DoesNotLogWhenCaptured(t *testing.T) {
	// The previous behavior double-logged: once inside RecoverError (without
	// request_id / kb_id context) and again at the call site that handled the
	// returned error. The new contract is that RecoverError stores the panic
	// (with embedded stack) into *errp and stays silent — the caller decides
	// how to surface it. Verify no log is emitted on panic when errp is set.
	h := installSignalHandler(t)
	var err error
	func() {
		defer RecoverError(&err)
		panic("captured")
	}()
	if err == nil {
		t.Fatal("expected error to be set")
	}
	if !strings.Contains(err.Error(), "captured") {
		t.Errorf("expected panic value embedded in error, got %q", err.Error())
	}
	// The embedded stack is essential — callers rely on it to surface the
	// origin of the panic without RecoverError holding the structured logger.
	if !strings.Contains(err.Error(), "goroutine") {
		t.Errorf("expected stack trace embedded in error, got %q", err.Error())
	}
	select {
	case r := <-h.records:
		t.Errorf("expected no log record when errp captures panic, got: %s", recordText(r))
	case <-time.After(50 * time.Millisecond):
	}
}
