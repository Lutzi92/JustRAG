package main

import (
	"log/slog"
	"os"

	"github.com/justrag/go-backend/internal/app"
	"github.com/justrag/go-backend/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := app.RunWorker(cfg); err != nil {
		slog.Error("worker error", "error", err)
		os.Exit(1)
	}
}
