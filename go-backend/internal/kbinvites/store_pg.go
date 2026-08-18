package kbinvites

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// ErrNotFound means the addressed link (or, on redeem, the token) does not
// exist. Revoked and never-existed are deliberately indistinguishable to the
// caller.
var ErrNotFound = errors.New("kbinvites: not found")

// Link is one invite link. CreatedByName is the username of whoever minted
// it, NULL once that account is deleted (created_by is ON DELETE SET NULL).
type Link struct {
	ID              string     `json:"id"              db:"id"`
	KBID            string     `json:"kbId"            db:"kb_id"`
	Token           string     `json:"token"           db:"token"`
	Role            string     `json:"role"            db:"role"`
	Label           *string    `json:"label"           db:"label"`
	CreatedByName   *string    `json:"createdByName"   db:"created_by_name"`
	CreatedAt       time.Time  `json:"createdAt"       db:"created_at"`
	RedemptionCount int        `json:"redemptionCount" db:"redemption_count"`
	LastUsedAt      *time.Time `json:"lastUsedAt"      db:"last_used_at"`
}

// RedeemResult describes the outcome of redeeming a token. Role is the
// caller's role on the KB AFTER redeeming, which is not necessarily the
// link's role — an existing stronger role wins.
type RedeemResult struct {
	KBID          string `json:"kbId"`
	KBName        string `json:"kbName"`
	Role          string `json:"role"`
	AlreadyMember bool   `json:"alreadyMember"`
}

// Store is the invite-link data layer. PGStore is its only implementation.
type Store interface {
	List(ctx context.Context, kbID string) ([]Link, error)
	Create(ctx context.Context, kbID, token, role string, label *string, createdBy string) (Link, error)
	Delete(ctx context.Context, kbID, linkID string) error
	Redeem(ctx context.Context, token, userID string) (RedeemResult, error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main pool.
func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

var _ Store = (*PGStore)(nil)

const linkCols = `l.id, l.kb_id, l.token, l.role, l.label, u.username AS created_by_name,
                  l.created_at, l.redemption_count, l.last_used_at`

// List returns a KB's links, newest first.
func (s *PGStore) List(ctx context.Context, kbID string) ([]Link, error) {
	sql := `SELECT ` + linkCols + `
	        FROM kb_invite_links l
	        LEFT JOIN users u ON u.id = l.created_by
	        WHERE l.kb_id = $1::uuid
	        ORDER BY l.created_at DESC`
	return pgxutil.QueryRows[Link](ctx, s.pool, sql, kbID)
}

// Create inserts a link. role must already have passed kbaccess.Assignable
// in the handler; the table's CHECK constraint is the backstop.
func (s *PGStore) Create(ctx context.Context, kbID, token, role string, label *string, createdBy string) (Link, error) {
	sql := `WITH ins AS (
	            INSERT INTO kb_invite_links (kb_id, token, role, label, created_by)
	            VALUES ($1::uuid, $2, $3, $4, NULLIF($5,'')::uuid)
	            RETURNING *
	        )
	        SELECT ` + linkCols + `
	        FROM ins l LEFT JOIN users u ON u.id = l.created_by`
	rows, err := pgxutil.QueryRows[Link](ctx, s.pool, sql, kbID, token, role, label, createdBy)
	if err != nil {
		return Link{}, fmt.Errorf("Create: %w", err)
	}
	if len(rows) == 0 {
		return Link{}, fmt.Errorf("Create: insert returned no row")
	}
	return rows[0], nil
}

// Delete revokes a link. The kb_id predicate is load-bearing: without it a
// link id from another KB would be deletable by any admin of any KB.
func (s *PGStore) Delete(ctx context.Context, kbID, linkID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM kb_invite_links WHERE id = $1::uuid AND kb_id = $2::uuid`, linkID, kbID)
	if err != nil {
		return fmt.Errorf("Delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// roleRankSQL maps a role column onto its ordinal, mirroring
// kbaccess.Rank. Inlined in SQL rather than compared in Go so the
// never-downgrade rule holds inside the same statement that writes the row —
// the same reason kbmembers.SetRole keeps the owner guard in its WHERE.
const roleRankSQL = `CASE %s WHEN 'view' THEN 0 WHEN 'edit' THEN 1
                              WHEN 'admin' THEN 2 WHEN 'owner' THEN 3 ELSE -1 END`

// Redeem applies a token for userID: it raises the caller's KB role to the
// link's role (never lowers it, never touches an owner row), clears a stale
// opt-out so the KB actually shows up, and counts the redemption. All of it
// in one transaction — a joiner who is granted membership but keeps an
// opted_out row would be a member of a KB they cannot see anywhere, which is
// exactly the state step 3 exists to prevent. kbmembers.LeaveKB is the
// precedent for one package owning a kb_members + kb_subscriptions
// transaction.
func (s *PGStore) Redeem(ctx context.Context, token, userID string) (RedeemResult, error) {
	var res RedeemResult
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var linkID, linkRole string
		err := tx.QueryRow(ctx, `
			SELECT l.id::text, l.kb_id::text, l.role, k.name
			FROM kb_invite_links l
			JOIN knowledge_bases k ON k.id = l.kb_id
			WHERE l.token = $1`, token).Scan(&linkID, &res.KBID, &linkRole, &res.KBName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("Redeem: lookup token: %w", err)
		}

		// Existing role decides both AlreadyMember and the role we report
		// back: an existing stronger role survives the upsert below, so
		// reading it first is what lets us answer accurately.
		var existing string
		err = tx.QueryRow(ctx,
			`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
			res.KBID, userID).Scan(&existing)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			existing = ""
		case err != nil:
			return fmt.Errorf("Redeem: read membership: %w", err)
		}
		res.AlreadyMember = existing != ""

		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO kb_members (kb_id, user_id, role, created_by)
			VALUES ($1::uuid, $2::uuid, $3, NULL)
			ON CONFLICT (kb_id, user_id) DO UPDATE
			    SET role = EXCLUDED.role
			    WHERE kb_members.role <> 'owner'
			      AND (%s) < (%s)`,
			fmt.Sprintf(roleRankSQL, "kb_members.role"),
			fmt.Sprintf(roleRankSQL, "EXCLUDED.role")),
			res.KBID, userID, linkRole); err != nil {
			return fmt.Errorf("Redeem: grant role: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM kb_subscriptions
			WHERE kb_id = $1::uuid AND user_id = $2::uuid AND state = 'opted_out'`,
			res.KBID, userID); err != nil {
			return fmt.Errorf("Redeem: clear opt-out: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE kb_invite_links
			SET redemption_count = redemption_count + 1, last_used_at = NOW()
			WHERE id = $1::uuid`, linkID); err != nil {
			return fmt.Errorf("Redeem: count redemption: %w", err)
		}

		// Report the role the caller actually holds now.
		if err := tx.QueryRow(ctx,
			`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
			res.KBID, userID).Scan(&res.Role); err != nil {
			return fmt.Errorf("Redeem: read resulting role: %w", err)
		}
		return nil
	})
	if err != nil {
		return RedeemResult{}, err
	}
	return res, nil
}
