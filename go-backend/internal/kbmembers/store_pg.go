// Package kbmembers implements the member-management data layer for the
// four-role KB permission model (view < edit < admin < owner): listing
// members, granting/changing/removing roles, transferring ownership, and
// letting a user leave a KB they belong to.
//
// The owner invariants — a role can never be assigned or removed on an
// owner row outside the explicit transfer — are enforced in SQL, not only
// in Go, so a second caller (a future handler, a script, another package)
// cannot bypass them by calling the store directly. See SetRole and
// RemoveMember.
package kbmembers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// Sentinel errors. Task 6 maps these to HTTP status codes via errors.Is.
var (
	// ErrNotFound means the target row (member, or in TransferOwner's case
	// the KB itself) does not exist.
	ErrNotFound = errors.New("kbmembers: not found")
	// ErrOwnerImmutable means the requested change would assign, demote, or
	// remove an owner role outside the explicit TransferOwner path.
	ErrOwnerImmutable = errors.New("kbmembers: owner role is immutable outside transfer")
	// ErrNotAMember means the acting user has no kb_members row for this KB.
	ErrNotAMember = errors.New("kbmembers: not a member")
)

// Member is one row of a KB's member listing, joined with display identity.
type Member struct {
	UserID    string    `json:"userId"    db:"user_id"`
	Username  string    `json:"username"  db:"username"`
	FirstName *string   `json:"firstName" db:"first_name"`
	LastName  *string   `json:"lastName"  db:"last_name"`
	Role      string    `json:"role"      db:"role"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// Store is the member-management data layer. PGStore is its only
// implementation.
type Store interface {
	ListMembers(ctx context.Context, kbID string) ([]Member, error)
	GetRole(ctx context.Context, kbID, userID string) (string, error)
	SetRole(ctx context.Context, kbID, userID, role, grantedBy string) error
	RemoveMember(ctx context.Context, kbID, userID string) error
	TransferOwner(ctx context.Context, kbID, newOwnerID string) error
	LeaveKB(ctx context.Context, kbID, userID string) (deletedChats int, err error)
	CountOwnChats(ctx context.Context, kbID, userID string) (int, error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// ListMembers returns every member with display names, strongest role first.
func (s *PGStore) ListMembers(ctx context.Context, kbID string) ([]Member, error) {
	const sql = `
		SELECT m.user_id, u.username, u.first_name, u.last_name, m.role, m.created_at
		FROM kb_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.kb_id = $1::uuid
		ORDER BY CASE m.role
		             WHEN 'owner' THEN 0 WHEN 'admin' THEN 1
		             WHEN 'edit'  THEN 2 ELSE 3 END,
		         u.username`
	return pgxutil.QueryRows[Member](ctx, s.pool, sql, kbID)
}

// GetRole returns the kb_members role for (kbID, userID), or ErrNotFound
// when the user has no row.
func (s *PGStore) GetRole(ctx context.Context, kbID, userID string) (string, error) {
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("GetRole: %w", err)
	}
	return role, nil
}

// SetRole grants or changes a non-owner role. The WHERE clause carries both
// invariants so no caller can bypass them: the target role must be assignable
// (checked by the caller via kbaccess.Assignable) and an existing owner row is
// never touched.
func (s *PGStore) SetRole(ctx context.Context, kbID, userID, role, grantedBy string) error {
	if !kbaccess.Assignable(role) {
		return ErrOwnerImmutable
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO kb_members (kb_id, user_id, role, created_by)
		VALUES ($1::uuid, $2::uuid, $3, NULLIF($4,'')::uuid)
		ON CONFLICT (kb_id, user_id) DO UPDATE
		    SET role = EXCLUDED.role
		    WHERE kb_members.role <> 'owner'`,
		kbID, userID, role, grantedBy)
	if err != nil {
		return fmt.Errorf("SetRole: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Der ON-CONFLICT-Zweig hat wegen der Owner-Bedingung nicht gegriffen.
		return ErrOwnerImmutable
	}
	return nil
}

// RemoveMember revokes someone else's role. It deliberately leaves their chats
// alone: a revocation is an administrative act, and destroying another user's
// work on a mis-click must not be possible. Self-service leaving goes through
// LeaveKB, which does delete them.
func (s *PGStore) RemoveMember(ctx context.Context, kbID, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM kb_members
		 WHERE kb_id = $1::uuid AND user_id = $2::uuid AND role <> 'owner'`,
		kbID, userID)
	if err != nil {
		return fmt.Errorf("RemoveMember: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Entweder gab es keine Zeile, oder es war die Owner-Zeile. Ein
		// Folge-SELECT unterscheidet das fuer eine brauchbare Fehlermeldung.
		var role string
		err := s.pool.QueryRow(ctx,
			`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
			kbID, userID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("RemoveMember: classify: %w", err)
		}
		return ErrOwnerImmutable
	}
	return nil
}

// TransferOwner moves ownership in one transaction: the current owner is
// demoted to admin (so a transfer never silently revokes access) and the target
// is promoted. The owner mirror on knowledge_bases.user_id follows via trigger.
func (s *PGStore) TransferOwner(ctx context.Context, kbID, newOwnerID string) error {
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Reihenfolge zwingend: erst den alten Owner herabstufen, sonst
		// verletzt der zweite Schritt kb_members_owner_uniq.
		if _, err := tx.Exec(ctx,
			`UPDATE kb_members SET role = 'admin' WHERE kb_id = $1::uuid AND role = 'owner'`,
			kbID); err != nil {
			return fmt.Errorf("TransferOwner: demote previous owner: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO kb_members (kb_id, user_id, role)
			VALUES ($1::uuid, $2::uuid, 'owner')
			ON CONFLICT (kb_id, user_id) DO UPDATE SET role = 'owner'`,
			kbID, newOwnerID)
		if err != nil {
			return fmt.Errorf("TransferOwner: promote new owner: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// LeaveKB removes the caller's own membership and deletes their chats in this
// KB. Deliberately destructive and deliberately self-service only: an admin
// revoking someone's role must NOT delete their chats (see RemoveMember).
func (s *PGStore) LeaveKB(ctx context.Context, kbID, userID string) (int, error) {
	var deleted int
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx,
			`SELECT role FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
			kbID, userID).Scan(&role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotAMember
			}
			return fmt.Errorf("LeaveKB: read role: %w", err)
		}
		if role == kbaccess.RoleOwner {
			return ErrOwnerImmutable
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM chats WHERE kb_id = $1::uuid AND user_id = $2::uuid`, kbID, userID)
		if err != nil {
			return fmt.Errorf("LeaveKB: delete chats: %w", err)
		}
		deleted = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx,
			`DELETE FROM kb_members WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
			kbID, userID); err != nil {
			return fmt.Errorf("LeaveKB: delete membership: %w", err)
		}
		return nil
	})
	return deleted, err
}

// CountOwnChats backs the leave-confirmation dialog, which names how many chats
// the user is about to lose.
func (s *PGStore) CountOwnChats(ctx context.Context, kbID, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM chats WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, userID).Scan(&n)
	return n, err
}
