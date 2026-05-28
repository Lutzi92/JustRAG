package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCodeRunner struct {
	stdout string
	stderr string
	err    error
	gotDir string
}

func (f *fakeCodeRunner) Run(_ context.Context, dir string, _ CodeExecConfig) (string, string, error) {
	f.gotDir = dir
	return f.stdout, f.stderr, f.err
}

// TestCodeExec_DisabledByDefault: the gate callback governs whether
// the tool runs at all. With it returning false, every invocation
// must reject before touching the runner. This is the security
// guarantee — even a misconfigured runner can't accidentally
// execute when the operator hasn't opted in.
func TestCodeExec_DisabledByDefault(t *testing.T) {
	t.Parallel()
	runner := &fakeCodeRunner{stdout: "should not run"}
	tool := NewCodeExec(DefaultCodeExecConfig(), runner, func(context.Context) bool { return false })
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"code":"print(1)"}`),
	)
	if err == nil {
		t.Fatal("expected disabled error")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should mention 'disabled': %v", err)
	}
	if runner.gotDir != "" {
		t.Error("runner was called despite gate being closed")
	}
}

// TestCodeExec_RequiresCode: empty code rejects before the runner.
func TestCodeExec_RequiresCode(t *testing.T) {
	t.Parallel()
	runner := &fakeCodeRunner{}
	tool := NewCodeExec(DefaultCodeExecConfig(), runner, func(context.Context) bool { return true })
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"code":""}`))
	if err == nil {
		t.Error("expected 'code is required' error")
	}
	if runner.gotDir != "" {
		t.Error("runner was called despite empty code")
	}
}

// TestCodeExec_MaxCodeSize: oversized code rejects before tmpdir
// allocation. The 256 KB limit is a wiring guard — a real Python
// snippet is rarely above 10 KB, anything above is a planner bug
// or an LLM that's piping a CSV inline.
func TestCodeExec_MaxCodeSize(t *testing.T) {
	t.Parallel()
	runner := &fakeCodeRunner{}
	tool := NewCodeExec(DefaultCodeExecConfig(), runner, func(context.Context) bool { return true })
	huge := strings.Repeat("x", 257*1024)
	args, _ := json.Marshal(map[string]any{"code": huge})
	_, err := tool.Handler.Invoke(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got %v", err)
	}
	if runner.gotDir != "" {
		t.Error("runner was called for oversized code")
	}
}

// TestCodeExec_StdoutPropagated: a clean run surfaces stdout in
// the tool result's Text field, with a stdout_len meta entry.
func TestCodeExec_StdoutPropagated(t *testing.T) {
	t.Parallel()
	runner := &fakeCodeRunner{stdout: "42"}
	tool := NewCodeExec(DefaultCodeExecConfig(), runner, func(context.Context) bool { return true })
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"code":"print(42)"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "42" {
		t.Errorf("Text not propagated: %q", got.Text)
	}
	if got.Meta["stdout_len"].(int) != 2 {
		t.Errorf("stdout_len meta wrong: %v", got.Meta["stdout_len"])
	}
}

// TestCodeExec_StderrFirstOnError: a runner error (non-zero exit)
// must put stderr BEFORE stdout in Text — the LLM needs the
// traceback to fix its next attempt; an error with stdout-only
// output would hide the actual failure.
func TestCodeExec_StderrFirstOnError(t *testing.T) {
	t.Parallel()
	runner := &fakeCodeRunner{
		stdout: "partial output before crash",
		stderr: "Traceback: NameError: name 'foo' is not defined",
		err:    errors.New("exit status 1"),
	}
	tool := NewCodeExec(DefaultCodeExecConfig(), runner, func(context.Context) bool { return true })
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"code":"print(foo)"}`),
	)
	if err != nil {
		t.Fatalf("tool itself shouldn't error on runner failure: %v", err)
	}
	if !strings.HasPrefix(got.Text, "Traceback") {
		t.Errorf("stderr should come first on error, got: %q", got.Text)
	}
	if got.Meta["exit_error"] == nil {
		t.Errorf("exit_error meta missing on runner error")
	}
}

// TestCapBytes covers the truncation marker — important for the
// LLM to know the output was cut.
func TestCapBytes(t *testing.T) {
	t.Parallel()
	if got := capBytes([]byte("short"), 100); got != "short" {
		t.Errorf("under-cap input modified: %q", got)
	}
	got := capBytes([]byte(strings.Repeat("x", 200)), 100)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("over-cap missing truncation marker: %q", got[len(got)-30:])
	}
	if len(got) > 130 {
		t.Errorf("cap+marker too long: %d bytes", len(got))
	}
}

// TestDefaultCodeExecConfig pins the security defaults — drift
// here would silently relax the sandbox.
func TestDefaultCodeExecConfig(t *testing.T) {
	t.Parallel()
	c := DefaultCodeExecConfig()
	if c.WallClock != 10*time.Second {
		t.Errorf("WallClock default drift: %v", c.WallClock)
	}
	if c.MemoryMB != 256 {
		t.Errorf("MemoryMB default drift: %d", c.MemoryMB)
	}
	if c.Runtime != "runsc" {
		t.Errorf("Runtime default drift (must default to gVisor): %q", c.Runtime)
	}
}
