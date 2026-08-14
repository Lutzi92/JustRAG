// Package kbsubs owns a user's subscription state for public knowledge bases
// and the catalog query that lists them. A subscription is display comfort
// only — access to a published public KB comes from rule 4 of
// kbaccess.EffectiveRole and needs no row here.
package kbsubs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// The two states a stored subscription row can carry. A user with no row at
// all is governed by the KB's auto_subscribe flag.
const (
	StateSubscribed = "subscribed"
	StateOptedOut   = "opted_out"
)

// CatalogEntry is one card in the discovery popup.
type CatalogEntry struct {
	ID          string   `json:"id"          db:"id"`
	Name        string   `json:"name"        db:"name"`
	Description *string  `json:"description" db:"description"`
	Subscribed  bool     `json:"subscribed"  db:"subscribed"`
	CategoryIDs []string `json:"categoryIds" db:"category_ids"`
}

// Store is the subscription data layer. PGStore is its only implementation.
type Store interface {
	SetState(ctx context.Context, kbID, userID, state string) error
	Catalog(ctx context.Context, userID, query string, categoryIDs []string) ([]CatalogEntry, error)
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

// Catalog lists the public KBs this user may discover, annotated with whether
// they currently see each one in their overview. Filtering happens in SQL
// rather than in Go so the LIMIT applies to the filtered set.
//
// Rows are the published public KBs plus any *staged* one the caller holds a
// kb_members row on. That second arm is what makes the Favoriten star
// reversible: the star writes an opt-out (never a membership change), and
// without the arm a curator who un-favorited their own staged KB would have
// no surface left to find it on.
//
// `subscribed` mirrors ListGlobalKnowledgeBases exactly — opt-out loses to
// nothing, and absent one it is membership, an explicit subscription, or
// auto_subscribe — so the star in the catalog shows the state the overview
// actually has, and toggling it moves the KB between the two sections.
//
// The displayed description falls back to header_text: knowledge_bases.description
// has no editor in the UI, while header_text is the blurb an admin actually
// writes and the Favoriten card already renders. The search predicate spans
// all three columns for the same reason — searching a field nobody can fill
// looks like a broken search.
func (s *PGStore) Catalog(ctx context.Context, userID, query string, categoryIDs []string) ([]CatalogEntry, error) {
	const catalogLimit = 200

	sql := `
		SELECT kb.id::text, kb.name,
		       COALESCE(NULLIF(kb.description, ''), NULLIF(kb.header_text, '')) AS description,
		       (
		         NOT EXISTS (SELECT 1 FROM kb_subscriptions s
		                     WHERE s.kb_id = kb.id AND s.user_id = $1::uuid AND s.state = 'opted_out')
		         AND (
		               EXISTS (SELECT 1 FROM kb_members m
		                       WHERE m.kb_id = kb.id AND m.user_id = $1::uuid)
		            OR (kb.is_published AND (
		                  EXISTS (SELECT 1 FROM kb_subscriptions s
		                          WHERE s.kb_id = kb.id AND s.user_id = $1::uuid AND s.state = 'subscribed')
		                  OR kb.auto_subscribe
		               ))
		             )
		       ) AS subscribed,
		       COALESCE(
		         (SELECT array_agg(l.category_id::text) FROM kb_category_links l WHERE l.kb_id = kb.id),
		         ARRAY[]::text[]
		       ) AS category_ids
		FROM knowledge_bases kb
		WHERE kb.visibility = 'public'
		  AND (kb.is_published = true OR EXISTS (
		        SELECT 1 FROM kb_members m WHERE m.kb_id = kb.id AND m.user_id = $1::uuid
		      ))
		  AND ($2 = '' OR kb.name ILIKE '%' || $2 || '%'
		       OR COALESCE(kb.description, '') ILIKE '%' || $2 || '%'
		       OR COALESCE(kb.header_text, '') ILIKE '%' || $2 || '%')
		  AND (cardinality($3::uuid[]) = 0 OR EXISTS (
		        SELECT 1 FROM kb_category_links l
		        WHERE l.kb_id = kb.id AND l.category_id = ANY($3::uuid[])
		      ))
		ORDER BY kb.name
		LIMIT $4`

	if categoryIDs == nil {
		categoryIDs = []string{}
	}
	rows, err := pgxutil.QueryRows[CatalogEntry](ctx, s.pool, sql, userID, query, categoryIDs, catalogLimit)
	if err != nil {
		return nil, fmt.Errorf("Catalog: %w", err)
	}
	return rows, nil
}
