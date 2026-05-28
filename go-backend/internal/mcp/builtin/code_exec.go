package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/mcp"
)

// CodeExecArgs documents the code_exec tool input. The agent
// supplies a Python snippet; the wrapper writes it to a per-call
// temp dir, bind-mounts the dir read-only into a fresh gVisor
// container, runs `python /work/code.py`, and returns stdout +
// stderr (each capped at 64 KB).
type CodeExecArgs struct {
	Code string `json:"code"`
}

const codeExecInputSchema = `{
  "type": "object",
  "required": ["code"],
  "properties": {
    "code": { "type": "string", "description": "Python source. Stdlib + pandas + numpy preinstalled. No network. /tmp is a 64 MB tmpfs; the source is mounted read-only at /work/code.py." }
  }
}`

// CodeExecConfig pins the security boundary at construction time.
// All limits are passed to `docker run` as flags; the wrapper does
// no host-side enforcement. Operator MUST configure these before
// flipping chat_code_exec_enabled to true on any KB.
//
// Image: pin to a Hash-referenced tag in production. The default
// here ("python:3.13-slim") is an example for the operator to
// replace with `python:3.13-slim@sha256:...`.
//
// Runtime: gVisor (`runsc`). The wrapper passes `--runtime=runsc`
// to docker; docker must have runsc registered as a runtime in
// /etc/docker/daemon.json.
type CodeExecConfig struct {
	Image       string        // container image (operator overrides via chat_code_exec_image site_config)
	WallClock   time.Duration // cap on docker exec wall-clock; 10s default
	MemoryMB    int           // memory limit; 256 default
	CPUs        float64       // CPU limit; 1.0 default
	StdoutBytes int           // output cap per stream; 64 KB default
	Runtime     string        // "runsc" by default; "" passes no --runtime flag (insecure, for local dev only)
}

// DefaultCodeExecConfig matches the plan: 10s wall, 256 MB memory,
// 1 CPU, 64 KB output. Image and runtime values are placeholders —
// operator MUST review before enabling per KB.
func DefaultCodeExecConfig() CodeExecConfig {
	return CodeExecConfig{
		Image:       "python:3.13-slim",
		WallClock:   10 * time.Second,
		MemoryMB:    256,
		CPUs:        1.0,
		StdoutBytes: 64 * 1024,
		Runtime:     "runsc",
	}
}

// CodeExecRunner is the test seam. Production wires `dockerRunner`
// (which shells out to `docker run`); tests inject a fake.
type CodeExecRunner interface {
	Run(ctx context.Context, codeDir string, cfg CodeExecConfig) (stdout, stderr string, err error)
}

// dockerRunner spawns `docker run --runtime=runsc ...` with the
// limits from cfg and bind-mounts codeDir read-only at /work.
// Captures stdout / stderr separately, each capped via io.LimitReader.
//
// Why docker and not raw runsc: runsc on its own needs an OCI
// bundle (config.json + rootfs unpacked). Docker handles bundle
// generation + image pull, so the wrapper stays a one-liner per
// call. The `--runtime=runsc` flag tells docker to use gVisor
// instead of runc; the daemon must have runsc registered in
// /etc/docker/daemon.json.
type dockerRunner struct{}

func (dockerRunner) Run(ctx context.Context, codeDir string, cfg CodeExecConfig) (string, string, error) {
	args := []string{
		"run",
		"--rm",
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--memory=%dm", cfg.MemoryMB),
		fmt.Sprintf("--cpus=%g", cfg.CPUs),
		"--tmpfs=/tmp:size=64m",
		"-v", codeDir + ":/work:ro",
		"-w", "/work",
	}
	if cfg.Runtime != "" {
		args = append(args, "--runtime="+cfg.Runtime)
	}
	args = append(args, cfg.Image, "python", "/work/code.py")

	subCtx, cancel := context.WithTimeout(ctx, cfg.WallClock)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "docker", args...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()

	stdout := capBytes(outBuf.Bytes(), cfg.StdoutBytes)
	stderr := capBytes(errBuf.Bytes(), cfg.StdoutBytes)
	return stdout, stderr, runErr
}

func capBytes(b []byte, cap int) string {
	if cap <= 0 || len(b) <= cap {
		return string(b)
	}
	out := make([]byte, cap+len("\n[truncated]"))
	copy(out, b[:cap])
	copy(out[cap:], "\n[truncated]")
	return string(out)
}

// NewCodeExec builds the code_exec tool. The runner is the test
// seam — production callers use NewCodeExec(DefaultCodeExecConfig(), nil)
// which wires the docker-based runner. The site_config gate
// (`chat_code_exec_enabled`) is checked at the tool level via the
// `enabled` callback; default false means the tool returns a
// "disabled" error on every invocation until an operator opts in.
func NewCodeExec(cfg CodeExecConfig, runner CodeExecRunner, enabled func(ctx context.Context) bool) mcp.Tool {
	if runner == nil {
		runner = dockerRunner{}
	}
	if enabled == nil {
		enabled = func(context.Context) bool { return false }
	}
	return mcp.Tool{
		Name:        "code_exec",
		Description: "Execute a Python snippet in a sandboxed gVisor container. No network. Stdlib + pandas + numpy. Use for deterministic data work the LLM keeps mishandling: row aggregation, date arithmetic, regex extraction across thousands of rows. Output capped at 64 KB stdout + stderr.",
		InputSchema: json.RawMessage(codeExecInputSchema),
		Handler:     mcp.ToolHandlerFunc(codeExecHandler(cfg, runner, enabled)),
	}
}

func codeExecHandler(cfg CodeExecConfig, runner CodeExecRunner, enabled func(ctx context.Context) bool) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		if !enabled(ctx) {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: disabled (site_config chat_code_exec_enabled=false)")
		}
		var args CodeExecArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: parse args: %w", err)
		}
		code := strings.TrimSpace(args.Code)
		if code == "" {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: code is required")
		}
		// Hard cap on input size — refuse multi-MB scripts before
		// allocating tmpdir. The container itself enforces no-network
		// + read-only-fs, so a large script is "just" a memory waste.
		if len(code) > 256*1024 {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: code too large (max 256 KB)")
		}

		dir, err := os.MkdirTemp("", "justrag-codeexec-*")
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: mkdir: %w", err)
		}
		defer os.RemoveAll(dir)

		if err := os.WriteFile(filepath.Join(dir, "code.py"), []byte(code), 0o644); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("code_exec: write code: %w", err)
		}

		start := time.Now()
		stdout, stderr, runErr := runner.Run(ctx, dir, cfg)
		elapsed := time.Since(start)

		// Surface BOTH stdout and stderr to the LLM regardless of
		// runErr — a syntax error in the user code would set runErr
		// AND populate stderr with the traceback; the LLM needs the
		// traceback to fix its next attempt.
		meta := map[string]any{
			"duration_ms": elapsed.Milliseconds(),
			"stdout_len":  len(stdout),
			"stderr_len":  len(stderr),
			"image":       cfg.Image,
			"runtime":     cfg.Runtime,
			"timed_out":   ctx.Err() != nil,
		}
		text := stdout
		if runErr != nil {
			meta["exit_error"] = runErr.Error()
			// stderr typically carries the actionable detail; prepend
			// it before stdout so the LLM sees the failure first.
			text = stderr + "\n---\n" + stdout
		}
		return mcp.ToolResult{
			Text: text,
			Meta: meta,
		}, nil
	}
}

// readAtMost is unused in production (dockerRunner uses the buffer
// approach above) but kept as a stable utility for future stream-
// mode runners that want to terminate child processes early when
// output exceeds the cap. The buffer approach is simpler for a
// 10-second wall clock; the stream approach matters for longer
// budgets that need early-kill on runaway output.
func readAtMost(r io.Reader, n int) (string, error) {
	limited := io.LimitReader(r, int64(n))
	b, err := io.ReadAll(limited)
	return string(b), err
}

var _ = readAtMost // pin the symbol so lint doesn't drop it
