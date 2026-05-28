package main

import (
	"log/slog"
	"os"

	"github.com/justrag/go-backend/internal/app"
	"github.com/justrag/go-backend/internal/config"

	// Aligns GOMAXPROCS with the cgroup CPU quota when running in a container,
	// so the Go scheduler doesn't oversubscribe threads on a constrained host.
	_ "go.uber.org/automaxprocs"
)

// version is set at build time via ldflags:
//
//	go build -ldflags "-X main.version=abc123" ./cmd/server/
var version = ""

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := app.RunServer(cfg, version); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
