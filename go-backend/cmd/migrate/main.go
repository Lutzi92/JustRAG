// Binary migrate applies database migrations and seeds the admin user.
//
// Usage:
//
//	/app/migrate                  # Run all: main + vector migrations, vector table setup, seed
//	/app/migrate --status         # Print current migration versions
//	/app/migrate --seed-only      # Only seed the admin user
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/migrate"
	mainmigrations "github.com/justrag/go-backend/migrations/main"
	vectormigrations "github.com/justrag/go-backend/migrations/vector"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	status := flag.Bool("status", false, "print current migration versions and exit")
	seedOnly := flag.Bool("seed-only", false, "only seed the admin user")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *status {
		if err := migrate.Status(ctx, cfg.DB, mainmigrations.FS, "main"); err != nil {
			slog.Error("main DB status failed", "error", err)
		}
		if err := migrate.Status(ctx, cfg.VectorDB, vectormigrations.FS, "vector"); err != nil {
			slog.Error("vector DB status failed", "error", err)
		}
		return
	}

	if *seedOnly {
		if err := migrate.Seed(ctx, cfg.DB); err != nil {
			slog.Error("seed failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Full run: migrations → vector setup → seed.
	slog.Info("running main database migrations")
	if err := migrate.RunMain(ctx, cfg.DB, mainmigrations.FS); err != nil {
		slog.Error("main migrations failed", "error", err)
		os.Exit(1)
	}

	// Vector migrations include DROP TABLE statements for main-schema tables
	// (cleanup from Drizzle's original shared-schema setup). Only run them
	// when the vector DB is a separate database, otherwise they destroy the
	// main schema we just migrated.
	if migrate.IsSameDatabase(cfg.DB, cfg.VectorDB) {
		slog.Info("vector DB shares main DB — skipping vector migrations (tables are co-located)")
	} else {
		slog.Info("running vector database migrations")
		if err := migrate.RunVector(ctx, cfg.VectorDB, vectormigrations.FS); err != nil {
			slog.Error("vector migrations failed", "error", err)
			os.Exit(1)
		}
	}

	slog.Info("ensuring vector tables")
	if err := migrate.EnsureVectorTables(ctx, cfg.DB, cfg.VectorDB); err != nil {
		slog.Error("vector table setup failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seeding database")
	if err := migrate.Seed(ctx, cfg.DB); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}

	slog.Info("all migrations complete")
}
