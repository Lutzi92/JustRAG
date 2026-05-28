package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/justrag/go-backend/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// Cost 12 (~250ms) defends the admin password — a hand-picked low-entropy
// secret — against offline brute-force if the hash leaks. Seed runs once at
// migrate-time so the latency is irrelevant. Login (authhandler) uses
// CompareHashAndPassword which reads cost from the stored hash, so existing
// cost-10 admin hashes from prior deployments keep working.
const bcryptCost = 12

// Seed ensures the admin user exists and has the correct password and role.
// The admin password is read from the ADMIN_PASSWORD env var.
// If the user already exists, their password and role are updated.
func Seed(ctx context.Context, cfg config.DBConfig) error {
	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		slog.Info("ADMIN_PASSWORD not set, skipping seed")
		return nil
	}

	db, err := openSQL(ctx, cfg)
	if err != nil {
		return fmt.Errorf("seed: open db: %w", err)
	}
	defer db.Close()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("seed: hash password: %w", err)
	}

	// Upsert in one statement so two concurrent migrate invocations (k8s
	// init-containers, rolling restarts where an old pod still runs) cannot
	// both observe a missing admin row and race on a unique-constraint
	// violation. ON CONFLICT also collapses the create vs. update branches
	// into a single code path.
	if _, err = db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, first_name, last_name, role)
		 VALUES ('admin', $1, 'Admin', 'User', 'superadmin')
		 ON CONFLICT (username) DO UPDATE
		     SET password_hash = EXCLUDED.password_hash,
		         role          = 'superadmin'`, string(hash)); err != nil {
		return fmt.Errorf("seed: upsert admin: %w", err)
	}
	slog.Info("admin user seeded")

	return nil
}
