package main

import (
	"log/slog"
	"os"

	"github.com/justrag/go-backend/internal/app"
	"github.com/justrag/go-backend/internal/config"

	// The runtime image (alpine, no tzdata package) has no zoneinfo database,
	// so time.LoadLocation("Europe/Berlin") would fail and every wall-clock
	// decision — the night sync window, the chat date line — would silently
	// run in UTC. Embedding the database costs ~450 KB per binary.
	_ "time/tzdata"
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
