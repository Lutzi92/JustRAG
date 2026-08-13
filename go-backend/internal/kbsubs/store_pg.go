// Package kbsubs owns a user's subscription state for public knowledge bases
// and the catalog query that lists them. A subscription is display comfort
// only — access to a published public KB comes from rule 4 of
// kbaccess.EffectiveRole and needs no row here.
package kbsubs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The two states a stored subscription row can carry. A user with no row at
// all is governed by the KB's auto_subscribe flag.
const (
	StateSubscribed = "subscribed"
	StateOptedOut   = "opted_out"
)

// Store is the subscription data layer. PGStore is its only implementation.
type Store interface {
	SetState(ctx context.Context, kbID, userID, state string) error
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

// SetState upserts the caller's subscription row. Opting out is stored rather
// than deleted: the row is what overrides a KB's auto_subscribe flag for this
// user, so deleting it would let the tile reappear on the next page load.
func (s *PGStore) SetState(ctx context.Context, kbID, userID, state string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO kb_subscriptions (kb_id, user_id, state)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (kb_id, user_id) DO UPDATE SET state = EXCLUDED.state`,
		kbID, userID, state)
	if err != nil {
		return fmt.Errorf("SetState(%s): %w", state, err)
	}
	return nil
}
