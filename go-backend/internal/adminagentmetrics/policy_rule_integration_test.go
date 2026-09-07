//go:build integration

// Verifies migration 0073's agent_decisions.policy_rule round-trips through
// PgStore.Insert / PgStore.Record against a live main Postgres. Skipped when
// DB_* env is unset; note the repo .env sets DB_HOST=db, which only resolves
// inside compose — run with DB_HOST=localhost or these tests do not run.

package adminagentmetrics

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("adminagentmetrics integration tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertKB creates a throwaway KB: agent_decisions.kb_id carries an FK to
// knowledge_bases.
func insertKB(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	name := "agentmetrics-" + t.Name() + "-" + uuid.NewString()
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO knowledge_bases (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, id)
	})
	return id
}

// readPolicyRule reads back the single agent_decisions row for kbID. Returns
// (value, isNull).
func readPolicyRule(t *testing.T, pool *pgxpool.Pool, kbID uuid.UUID) (int, bool) {
	t.Helper()
	var v *int
	if err := pool.QueryRow(context.Background(),
		`SELECT policy_rule FROM agent_decisions WHERE kb_id = $1`, kbID).Scan(&v); err != nil {
		t.Fatalf("select policy_rule: %v", err)
	}
	if v == nil {
		return 0, true
	}
	return *v, false
}

// W6-R6 / migration 0073: a policy rule index written through Record survives
// the round trip as a non-NULL smallint.
func TestPgStore_RecordPersistsPolicyRule(t *testing.T) {
	pool := integrationPool(t)
	store := NewPgStore(pool)
	kbID := insertKB(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_decisions WHERE kb_id = $1`, kbID)
	})

	rule := 2
	store.Record(context.Background(), kbID.String(), "supervisor", "answered", 0, 0, 42, nil, nil, nil, &rule)

	got, isNull := readPolicyRule(t, pool, kbID)
	if isNull {
		t.Fatal("policy_rule is NULL, want 2")
	}
	if got != rule {
		t.Fatalf("policy_rule = %d, want %d", got, rule)
	}
}

// Rule 0 must land as 0, not as NULL — the pointer is what distinguishes
// "rule 0 pinned this turn" from "the ladder decided".
func TestPgStore_InsertPersistsPolicyRuleZero(t *testing.T) {
	pool := integrationPool(t)
	store := NewPgStore(pool)
	kbID := insertKB(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_decisions WHERE kb_id = $1`, kbID)
	})

	zero := 0
	if err := store.Insert(context.Background(), DecisionRow{
		KbID: kbID, Mode: "agentic", Outcome: "answered", PolicyRule: &zero,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, isNull := readPolicyRule(t, pool, kbID)
	if isNull {
		t.Fatal("policy_rule is NULL, want 0")
	}
	if got != 0 {
		t.Fatalf("policy_rule = %d, want 0", got)
	}
}

// The ladder-decided case stays SQL NULL.
func TestPgStore_InsertLeavesPolicyRuleNull(t *testing.T) {
	pool := integrationPool(t)
	store := NewPgStore(pool)
	kbID := insertKB(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_decisions WHERE kb_id = $1`, kbID)
	})

	if err := store.Insert(context.Background(), DecisionRow{
		KbID: kbID, Mode: "crag", Outcome: "answered",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if _, isNull := readPolicyRule(t, pool, kbID); !isNull {
		t.Fatal("policy_rule is not NULL, want NULL for a ladder-decided turn")
	}
}
