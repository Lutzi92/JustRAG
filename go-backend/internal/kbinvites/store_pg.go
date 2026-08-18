package kbinvites

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// Redeem is implemented in the next task. Declared now so PGStore satisfies
// Store and the package compiles.
func (s *PGStore) Redeem(ctx context.Context, token, userID string) (RedeemResult, error) {
	return RedeemResult{}, errors.New("kbinvites: Redeem not implemented")
}
