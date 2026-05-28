// Package app contains the startup wiring shared between cmd/server and
// cmd/worker: it builds the dependency graph from a *config.Config, registers
// HTTP routes, starts the asynq worker pool, and runs the background
// scheduler (token-blacklist GC, query-cache sweeper, etc.).
//
// The split-by-file layout reflects that wiring:
//
//   - server.go / worker.go: top-level RunServer / RunWorker entrypoints.
//   - infra.go: shared infrastructure (DB pools, Redis, S3, MCP registry,
//     site_configs reader) plus the small "deps" adapter structs that
//     satisfy package-local handler interfaces via embedding.
//   - routes.go: HTTP route registration grouped by feature area; this is
//     the single source of truth for which handler is mounted where.
//   - scheduler.go / signals.go / query_cache_sweeper.go: lifecycle
//     management for background goroutines.
//
// This package is intentionally the only one that imports both the
// individual handler packages and the storage/AI packages — keeping that
// import edge here lets every other internal/ package stay focused on a
// single concern.
package app
