package app

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/safego"
)

// pprof handler wrappers — expose net/http/pprof on the internal debug port.
var (
	pprofIndex   = pprof.Index
	pprofCmdline = pprof.Cmdline
	pprofProfile = pprof.Profile
	pprofSymbol  = pprof.Symbol
	pprofTrace   = pprof.Trace
)

// startPprofServer starts the debug profiling endpoint when PPROF_ENABLED is
// set, and returns the server so the caller can Close it during shutdown.
// Returns nil when profiling is disabled — callers must nil-check.
//
// Shared by RunServer and RunWorker. The worker is the more valuable of the
// two to profile (chunking, embedding batching, RAPTOR/community build,
// PageRank) and is the process a `default.pgo` profile should be captured
// from, since PGO optimises the binary whose profile it was built with.
//
// Both processes default to 127.0.0.1:6060; when they share a network
// namespace, set PPROF_ADDR on one of them so the second binding doesn't fail
// (a failed bind only warns — it never takes the process down).
func startPprofServer(cfg config.PprofConfig) *http.Server {
	if !cfg.Enabled {
		return nil
	}

	// Mutex/block profile sampling rates are operator-configurable via
	// PPROF_MUTEX_PROFILE_FRACTION / PPROF_BLOCK_PROFILE_RATE; both
	// default to 0 (off) so /debug/pprof/mutex and /debug/pprof/block
	// return empty profiles on an always-on pprof endpoint with no
	// runtime overhead. Operators bump them for targeted sessions —
	// typical values are 5 (1-in-5 contentions) and 1000 (blocks ≥ 1µs);
	// higher rates add measurable overhead under heavy contention.
	if cfg.MutexProfileFraction > 0 {
		runtime.SetMutexProfileFraction(cfg.MutexProfileFraction)
	}
	if cfg.BlockProfileRate > 0 {
		runtime.SetBlockProfileRate(cfg.BlockProfileRate)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprofIndex)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprofCmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprofProfile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprofSymbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprofTrace)
	// Index serves /debug/pprof/heap, /goroutine, /mutex, /block via
	// the catch-all "GET /debug/pprof/" route above. No extra HandleFunc
	// needed for them — pprof.Index dispatches by basename.
	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
		// Bound how long a slow client may take to send request headers
		// so a wedged debug session can't pin a goroutine indefinitely.
		// /debug/pprof/profile is the only long-lived endpoint and it
		// streams response bytes (driven by ?seconds=), so the header
		// deadline is independent of profile duration.
		ReadHeaderTimeout: 10 * time.Second,
	}
	safego.Go(func() {
		slog.Info("pprof server listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server error", "error", err)
		}
	})
	return srv
}
