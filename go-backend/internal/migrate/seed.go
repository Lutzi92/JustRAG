package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/justrag/go-backend/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// Cost 12 (~250ms) defends the seeded passwords — hand-picked low-entropy
// secrets — against offline brute-force if the hash leaks. Seed runs once at
// migrate-time so the latency is irrelevant. Login (authhandler) uses
// CompareHashAndPassword which reads cost from the stored hash, so existing
// cost-10 admin hashes from prior deployments keep working.
const bcryptCost = 12

// seedUser is one row Seed upserts. Each is gated on its own env var, so an
// unset password skips that user without affecting the others.
type seedUser struct {
	username  string
	firstName string
	lastName  string
	role      string
	password  string
	// resetRole overwrites the role of an already-existing row. True for admin
	// so a demoted or locked-out superadmin is recoverable by re-running
	// migrate. False for tester: promoting it in the admin UI is exactly what
	// the account is for (checking a privileged view), and a re-seed must not
	// silently undo that.
	resetRole bool
}

// seedUsers returns the users to upsert, in a deterministic order. getenv is a
// parameter rather than a direct os.Getenv call so the gating is testable
// without touching process environment.
func seedUsers(getenv func(string) string) []seedUser {
	var users []seedUser
	if pw := getenv("ADMIN_PASSWORD"); pw != "" {
		users = append(users, seedUser{
			username: "admin", firstName: "Admin", lastName: "User",
			role: "superadmin", password: pw, resetRole: true,
		})
	}
	// A non-privileged account for local testing: it makes the ordinary-user
	// view reachable without demoting admin. Left unset (the shipped default
	// outside local dev) no tester row is created at all.
	if pw := getenv("TESTER_PASSWORD"); pw != "" {
		users = append(users, seedUser{
			username: "tester", firstName: "Test", lastName: "User",
			role: "user", password: pw, resetRole: false,
		})
	}
	return users
}

// seedUpsertSQL builds the upsert for one seed user. Upserting in a single
// statement means two concurrent migrate invocations (k8s init-containers,
// rolling restarts where an old pod still runs) cannot both observe a missing
// row and race on a unique-constraint violation. ON CONFLICT also collapses
// the create vs. update branches into a single code path.
func seedUpsertSQL(u seedUser) string {
	const insert = `INSERT INTO users (username, password_hash, first_name, last_name, role)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (username) DO UPDATE
		     SET password_hash = EXCLUDED.password_hash`
	if u.resetRole {
		return insert + `,
		         role          = EXCLUDED.role`
	}
	return insert
}

// Seed ensures the seeded users exist and have the configured password.
// Passwords are read from ADMIN_PASSWORD and TESTER_PASSWORD; a user whose
// var is empty is skipped. An existing row always has its password refreshed;
// its role is overwritten only for admin (see seedUser.resetRole).
func Seed(ctx context.Context, cfg config.DBConfig) error {
	users := seedUsers(os.Getenv)
	if len(users) == 0 {
		slog.Info("no seed passwords set, skipping seed")
		return nil
	}

	db, err := openSQL(ctx, cfg)
	if err != nil {
		return fmt.Errorf("seed: open db: %w", err)
	}
	defer db.Close()

	for _, u := range users {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.password), bcryptCost)
		if err != nil {
			return fmt.Errorf("seed: hash password for %s: %w", u.username, err)
		}
		if _, err = db.ExecContext(ctx, seedUpsertSQL(u),
			u.username, string(hash), u.firstName, u.lastName, u.role); err != nil {
			return fmt.Errorf("seed: upsert %s: %w", u.username, err)
		}
		slog.Info("user seeded", "username", u.username, "role", u.role)
	}

	return nil
}
