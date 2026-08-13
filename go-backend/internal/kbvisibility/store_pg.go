// Package kbvisibility owns the two transactions that move a knowledge base
// between private and public. Both are single-purpose and transactional
// because they touch three things at once — the KB row, the owner membership
// and (on unpublish) every subscription — and a partial failure would leave a
// KB with two owners or none.
package kbvisibility

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

var (
	// ErrNotFound means no knowledge base with that id exists.
	ErrNotFound = errors.New("kbvisibility: knowledge base not found")
	// ErrAlreadyPublic means Publish was called on a public KB.
	ErrAlreadyPublic = errors.New("kbvisibility: knowledge base is already public")
	// ErrNotPublic means Unpublish was called on a private KB.
	ErrNotPublic = errors.New("kbvisibility: knowledge base is not public")
	// ErrOwnerNotEligible means the requested new owner has no kb_members row.
	ErrOwnerNotEligible = errors.New("kbvisibility: new owner must be an existing member")
)

// Candidate is a user eligible to take ownership when a KB is unpublished:
// the KB's current admins.
type Candidate struct {
	UserID    string  `json:"userId"    db:"user_id"`
	Username  string  `json:"username"  db:"username"`
	FirstName *string `json:"firstName" db:"first_name"`
	LastName  *string `json:"lastName"  db:"last_name"`
}

// Impact backs the unpublish confirmation dialog: how many subscribers lose
// the KB from their overview, and who can take it over.
type Impact struct {
	Subscribers int         `json:"subscribers"`
	Candidates  []Candidate `json:"candidates"`
}

// Store is the visibility data layer. PGStore is its only implementation.
type Store interface {
	Publish(ctx context.Context, kbID string) error
	Unpublish(ctx context.Context, kbID, newOwnerID string) error
	UnpublishImpact(ctx context.Context, kbID string) (Impact, error)
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

// Publish turns a private KB into a public one. A public KB has no owner: the
// previous owner keeps full access as a KB admin, and system admins are
// covered implicitly by rule 3 of kbaccess.EffectiveRole.
//
// Publishing is staged: it also forces is_published = false, so a freshly
// published KB is public-but-not-yet-live — visible only to its KB members
// (any role, incl. the just-demoted ex-owner) and to system admins, until an
// operator flips the catalog toggle in the admin tab. This is delivered by
// kb.ListGlobalKnowledgeBases's non-admin overview query, whose kb_members
// EXISTS arm is OR'd ahead of, and independent of, is_published — that arm is
// what makes a member see the KB while it stays staged; the subscription and
// auto_subscribe arms still require is_published = true, so an ordinary
// subscriber never does. Two reasons is_published is set here rather than
// left alone. It DEFAULTS to true (migration 0012), so without the write a
// first publish would go world-readable the instant the button is clicked,
// with no chance to fill or proof-read the KB — the spec's „erst befüllt und
// getestet … dann live". And Unpublish sets is_published = false, so
// re-publishing a KB that was ever unpublished would otherwise land in a
// silently invisible state that nothing resets. Setting it here makes
// publish and unpublish symmetric.
//
// The explicit user_id = NULL is not redundant. Migration 0064's
// kb_members_sync_owner_trg fires only WHEN (NEW.role = 'owner'); demoting the
// owner to admin does not satisfy that clause, so the mirror on
// knowledge_bases.user_id would keep pointing at the ex-owner and the ~40
// owner-display joins would name a person who no longer owns the KB.
func (s *PGStore) Publish(ctx context.Context, kbID string) error {
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_bases SET visibility = 'public', is_published = false
			WHERE id = $1::uuid AND visibility = 'private'`, kbID)
		if err != nil {
			return fmt.Errorf("Publish: set visibility: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return classifyMissing(ctx, tx, kbID, ErrAlreadyPublic)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE kb_members SET role = 'admin'
			WHERE kb_id = $1::uuid AND role = 'owner'`, kbID); err != nil {
			return fmt.Errorf("Publish: demote owner: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE knowledge_bases SET user_id = NULL WHERE id = $1::uuid`, kbID); err != nil {
			return fmt.Errorf("Publish: clear owner mirror: %w", err)
		}
		return nil
	})
}

// Unpublish turns a public KB back into a private one owned by newOwnerID.
// Callers pick newOwnerID from UnpublishImpact's candidates (or themselves
// when there are none); this function only verifies that the target ends up
// with a row.
//
// Subscriptions are deleted rather than kept: the KB is no longer discoverable
// and the rows would be unreachable clutter. Chats are deliberately NOT
// deleted — losing access is not the same as asking to be removed.
func (s *PGStore) Unpublish(ctx context.Context, kbID, newOwnerID string) error {
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_bases
			SET visibility = 'private', auto_subscribe = false, is_published = false
			WHERE id = $1::uuid AND visibility = 'public'`, kbID)
		if err != nil {
			return fmt.Errorf("Unpublish: set visibility: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return classifyMissing(ctx, tx, kbID, ErrNotPublic)
		}

		// Defensiv: eine oeffentliche KB sollte keine Owner-Zeile haben, aber
		// eine stehengebliebene wuerde die Promotion unten an
		// kb_members_owner_uniq scheitern lassen.
		if _, err := tx.Exec(ctx, `
			UPDATE kb_members SET role = 'admin'
			WHERE kb_id = $1::uuid AND role = 'owner'`, kbID); err != nil {
			return fmt.Errorf("Unpublish: clear stray owner: %w", err)
		}

		// Promotion auf 'owner' erfuellt die WHEN-Klausel des Triggers, der den
		// Spiegel knowledge_bases.user_id automatisch nachzieht — anders als
		// beim Veroeffentlichen ist hier nichts von Hand zu tun.
		if _, err := tx.Exec(ctx, `
			INSERT INTO kb_members (kb_id, user_id, role)
			VALUES ($1::uuid, $2::uuid, 'owner')
			ON CONFLICT (kb_id, user_id) DO UPDATE SET role = 'owner'`,
			kbID, newOwnerID); err != nil {
			return fmt.Errorf("Unpublish: promote new owner: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`DELETE FROM kb_subscriptions WHERE kb_id = $1::uuid`, kbID); err != nil {
			return fmt.Errorf("Unpublish: drop subscriptions: %w", err)
		}
		return nil
	})
}

// UnpublishImpact reports what the unpublish dialog must state before the
// operator confirms.
func (s *PGStore) UnpublishImpact(ctx context.Context, kbID string) (Impact, error) {
	var impact Impact

	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM kb_subscriptions
		WHERE kb_id = $1::uuid AND state = 'subscribed'`, kbID).Scan(&impact.Subscribers)
	if err != nil {
		return Impact{}, fmt.Errorf("UnpublishImpact: count subscribers: %w", err)
	}

	candidates, err := pgxutil.QueryRows[Candidate](ctx, s.pool, `
		SELECT m.user_id, u.username, u.first_name, u.last_name
		FROM kb_members m JOIN users u ON u.id = m.user_id
		WHERE m.kb_id = $1::uuid AND m.role = 'admin'
		ORDER BY u.username`, kbID)
	if err != nil {
		return Impact{}, fmt.Errorf("UnpublishImpact: list candidates: %w", err)
	}
	impact.Candidates = candidates
	return impact, nil
}

// classifyMissing distinguishes "no such KB" from "wrong state" after a
// guarded UPDATE affected no rows, so the handler can answer 404 vs 409.
func classifyMissing(ctx context.Context, tx pgx.Tx, kbID string, wrongState error) error {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT true FROM knowledge_bases WHERE id = $1::uuid`, kbID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}
	return wrongState
}
