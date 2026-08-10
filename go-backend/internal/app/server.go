package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/middleware"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// isUploadExempt reports whether r targets one of the upload routes that set
// their own larger http.MaxBytesReader / multipart caps internally and are
// therefore exempt from the global request-body cap. The list is exhaustive
// on purpose: every other route — including all other /api/kb/ routes —
// gets the global cap, so a handler reading r.Body raw can never be
// unbounded just because it lives under an upload route's prefix.
func isUploadExempt(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/site-config/logo", // logo upload (siteconfig.maxLogoSize)
		"/api/describe-image",         // vision input, 10 MiB (misc.maxImageBytes)
		"/api/admin/agent/template",   // template upload, 10 MiB (adminmaintenance)
		"/api/admin/eval/golden-sets": // golden-set JSONL upload, 5 MiB (admineval)
		return true
	}
	// POST /api/kb/{id}/files (500 MiB) and POST /api/kb/{id}/eval/golden-sets
	// (5 MiB): the {id} segment sits mid-path, so match the shape explicitly.
	// Deeper paths (e.g. .../eval/golden-sets/generate) deliberately don't match.
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/kb/")
	if !ok {
		return false
	}
	id, tail, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return false
	}
	return tail == "files" || tail == "eval/golden-sets"
}

// RunServer starts the HTTP server with all wiring. It blocks until the server
// shuts down and returns any fatal error. The version string is typically set
// via ldflags at build time.
func RunServer(cfg *config.Config, version string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracingShutdown, err := observability.InitTracing(ctx, "justrag-server", version)
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracingShutdown(shutdownCtx); err != nil {
			slog.Warn("tracing shutdown error", "error", err)
		}
	}()

	// Configure XFF parsing once before any rate-limiter handles a request.
	// With cfg.TrustedProxyHopCount = 0 (default), XFF / X-Real-IP are
	// ignored — required to prevent clients from spoofing the rate-limit
	// key. Operators behind a reverse proxy must set the env var explicitly.
	middleware.SetTrustedProxyHops(cfg.TrustedProxyHopCount)
	if cfg.IsProduction() && cfg.TrustedProxyHopCount == 0 {
		slog.Warn("TRUSTED_PROXY_HOP_COUNT=0 in production: rate limits will key on the TCP peer (the reverse proxy) and collapse all clients into one bucket. Set this to the number of trusted proxies in front of this server (typically 1).")
	}

	infra, cleanup, err := setupServerInfra(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	// Start RSS + Confluence schedulers under a Redis leader lock so exactly
	// one go-server replica drives scheduled syncs across the deployment.
	// Track the goroutine so cleanup() (which closes the asynq client) does
	// not race with an in-flight scheduler enqueue. Defers are LIFO, so this
	// Wait() runs BEFORE cleanup() above.
	var schedulerWg sync.WaitGroup
	schedulerWg.Add(1)
	defer schedulerWg.Wait()
	// Registered AFTER the Wait above so it runs BEFORE it (LIFO): an
	// early error return (e.g. setupRoutes failing) must cancel ctx
	// first, or the Wait blocks forever on a scheduler goroutine that
	// only exits on ctx.Done(). The outer `defer cancel()` at the top
	// of this function runs too late — after this Wait. Idempotent.
	defer cancel()
	safego.Go(func() {
		defer schedulerWg.Done()
		startSchedulers(ctx, infra)
	})

	buildVersion := version
	if buildVersion == "" {
		buildVersion = os.Getenv("BUILD_VERSION")
	}
	if buildVersion == "" {
		buildVersion = "unknown"
	}

	mux := http.NewServeMux()
	routeCleanup, err := setupRoutes(ctx, mux, infra, cfg, buildVersion)
	if err != nil {
		return fmt.Errorf("setup routes: %w", err)
	}
	defer routeCleanup()

	apiLimiter := middleware.NewRateLimiter(ctx, cfg.RateLimitAPI)
	defer apiLimiter.Shutdown()

	compress, err := middleware.Compression()
	if err != nil {
		return fmt.Errorf("compression middleware: %w", err)
	}

	var handler http.Handler = mux
	// Innermost: rewrite ServeMux's plain-text 404/405 defaults (and direct
	// http.NotFound calls) into the JSON error envelope on the API surface,
	// so every error body under these prefixes is {"error": "..."}.
	handler = middleware.JSONAPIErrors("/api/", "/openai/")(handler)
	// Outside JSONAPIErrors so rewritten error bodies are still compressible;
	// inside Logging/Metrics so recorded response sizes are wire bytes. SSE
	// (text/event-stream) passes through uncompressed — see Compression docs.
	handler = compress(handler)
	// Global body cap — only the upload routes enumerated in isUploadExempt
	// are exempt; each sets its own larger http.MaxBytesReader / multipart
	// cap internally.
	handler = middleware.MaxBytesExcept(cfg.MaxRequestBodyBytes, isUploadExempt)(handler)
	handler = apiLimiter.MiddlewareForPrefix("/api", handler)
	handler = middleware.MetricsMiddleware(handler)
	handler = middleware.Logging(cfg.LogVerbose)(handler)
	handler = otelhttp.NewMiddleware("justrag-server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + middleware.NormalizeRoute(r.URL.Path)
		}),
	)(handler)
	handler = middleware.RequestID(handler)
	handler = middleware.SecurityHeaders(cfg.IsProduction(), cfg.CSPHeader)(handler)
	handler = middleware.CORS(cfg.AllowedOrigins)(handler)
	handler = middleware.Recovery(handler)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: handler,
		// ReadTimeout protects against slow request bodies. 120s matches
		// DB.StatementTimeout — a request stalled longer than that is
		// already lost to the upstream DB anyway.
		ReadTimeout: 120 * time.Second,
		// SSE endpoints (chat streaming, research relay, public stream) opt out
		// of this deadline per-connection via http.NewResponseController. Keep
		// the global cap to protect non-streaming responses from stalling on
		// slow clients or wedged backend calls.
		WriteTimeout: 120 * time.Second,
		// 65s > 60s, the default ALB / nginx idle timeout, so the server
		// closes idle keep-alive connections AFTER the load balancer does.
		// If we closed first, the LB would RST a connection that already
		// has an in-flight request, surfacing as 502s in clients.
		IdleTimeout:       65 * time.Second,
		ReadHeaderTimeout: 65 * time.Second,
		// 64 KiB is generous for this API's headers (Bearer JWT + cookies,
		// single-digit KiB) while bounding header-buffer memory per
		// connection well below the stdlib's 1 MiB default.
		MaxHeaderBytes: 64 << 10,
	}

	// Start pprof server only when explicitly enabled via PPROF_ENABLED=true.
	// Binds to localhost only to prevent exposure on container/pod networks.
	// Access: go tool pprof http://localhost:6060/debug/pprof/heap
	pprofServer := startPprofServer(cfg.Pprof)

	sigCh, stopSignals := newShutdownSignals()
	defer stopSignals()

	// Track the signal handler so its lifecycle is explicit: defers are
	// LIFO, so this Wait() runs BEFORE setupServerInfra's cleanup, the
	// tracing shutdown, and the outer cancel() — preventing the handler
	// from touching server / pprofServer while their resources are being
	// torn down.
	var sigWg sync.WaitGroup
	sigWg.Add(1)
	defer sigWg.Wait()
	// Registered AFTER the Wait above so it runs BEFORE it (LIFO): the
	// signal goroutine's ctx.Done() escape hatch only works if ctx is
	// actually cancelled before we block on Wait. Without this, a
	// ListenAndServe failure (e.g. EADDRINUSE) deadlocks here until a
	// manual SIGTERM. Idempotent.
	defer cancel()
	// safego.Go provides panic recovery so a nil-deref during shutdown
	// (the worst possible moment to crash) doesn't bypass deferred cleanup.
	safego.Go(func() {
		defer sigWg.Done()
		// Escape on ctx.Done so an early ListenAndServe failure (e.g.
		// EADDRINUSE) doesn't leak this goroutine waiting for a signal
		// that will never arrive.
		select {
		case sig := <-sigCh:
			slog.Info("received shutdown signal", "signal", sig)
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()
			if pprofServer != nil {
				pprofServer.Close()
			}
			if err := server.Shutdown(shutdownCtx); err != nil {
				slog.Error("server shutdown error", "error", err)
			}
			cancel()
		case <-ctx.Done():
		}
	})

	slog.Info("Go server starting", "port", cfg.Port, "env", cfg.Env, "s3", cfg.S3.S3Enabled())
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	slog.Info("server stopped gracefully")
	return nil
}
