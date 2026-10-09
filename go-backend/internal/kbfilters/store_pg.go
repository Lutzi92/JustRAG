// Package kbfilters owns the two per-user topic filters behind the shell's
// chip row: favorites (a star on any topic the user can see) and the user's
// own categories plus their assignments.
//
// Both are display state, never an access grant — starring or tagging a
// topic the caller can already open grants nothing, and neither table is
// consulted by any access decision. Deliberately separate from
// internal/kbcategories: that is one system-admin-curated taxonomy over PUBLIC
// KBs (the catalog's filter tabs); these rows are owned by one user and may
// point at any topic that user can see.
//
// Favorites are also distinct from subscriptions (internal/kbsubs): a
// subscription decides whether a global topic is in the caller's overview at
// all, a favorite pins any topic — private or global — to the "Favoriten" chip.
package kbfilters

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// MaxCategoriesPerUser caps how many categories one user may own. The chip
// row is a single line of filters; fifty is far beyond any usable row and
// exists to bound one account's footprint in kb_user_categories, not to
// shape the UI.
const MaxCategoriesPerUser = 50

var (
	// ErrDuplicateName means the (user_id, LOWER(name)) unique index rejected
	// the write: this user already has a category with that name.
	ErrDuplicateName = errors.New("kbfilters: a category with that name already exists")
	// ErrCategoryLimit means the user already owns MaxCategoriesPerUser
	// categories, so the insert was not performed.
	ErrCategoryLimit = errors.New("kbfilters: category limit reached")
	// ErrNotFound means no category with that id exists *for this user*. The
	// store never distinguishes "does not exist" from "belongs to someone
	// else" — the handler answers 404 either way, so a probe cannot use the
	// status code to enumerate other users' category ids.
	ErrNotFound = errors.New("kbfilters: category not found")
)

// UserCategory is one chip in the caller's own filter row.
type UserCategory struct {
	ID        string `json:"id"        db:"id"`
	Name      string `json:"name"      db:"name"`
	SortOrder int32  `json:"sortOrder" db:"sort_order"`
}

// Store is the per-user filter data layer. PGStore is its only
// implementation. Every method takes the caller's user id: there is no
// "current user" in this layer, and no method can read or write another
// user's rows.
type Store interface {
	AddFavorite(ctx context.Context, userID, kbID string) error
	RemoveFavorite(ctx context.Context, userID, kbID string) error

	ListCategories(ctx context.Context, userID string) ([]UserCategory, error)
	CreateCategory(ctx context.Context, userID, name string, sortOrder int32) (*UserCategory, error)
	UpdateCategory(ctx context.Context, userID, catID, name string, sortOrder int32) (*UserCategory, error)
	DeleteCategory(ctx context.Context, userID, catID string) error

	AssignCategory(ctx context.Context, userID, catID, kbID string) error
	UnassignCategory(ctx context.Context, userID, catID, kbID string) error
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

// AddFavorite stars a KB for this user. Idempotent: the row's existence is
// the flag, so a second PUT must not be an error.
func (s *PGStore) AddFavorite(ctx context.Context, userID, kbID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO kb_favorites (user_id, kb_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (user_id, kb_id) DO NOTHING`, userID, kbID)
	if err != nil {
		return fmt.Errorf("AddFavorite: %w", err)
	}
	return nil
}

// RemoveFavorite un-stars a KB. Deleting rather than storing a false flag:
// unlike kb_subscriptions there is nothing for an absent row to override, so
// a "not favorite" row would be dead data.
func (s *PGStore) RemoveFavorite(ctx context.Context, userID, kbID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM kb_favorites WHERE user_id = $1::uuid AND kb_id = $2::uuid`, userID, kbID)
	if err != nil {
		return fmt.Errorf("RemoveFavorite: %w", err)
	}
	return nil
}

// ListCategories returns this user's categories in chip order.
func (s *PGStore) ListCategories(ctx context.Context, userID string) ([]UserCategory, error) {
	return pgxutil.QueryRows[UserCategory](ctx, s.pool, `
		SELECT id::text, name, sort_order
		FROM kb_user_categories
		WHERE user_id = $1::uuid
		ORDER BY sort_order, name`, userID)
}

// CreateCategory inserts a category owned by userID, mapping the
// case-insensitive unique-name violation to ErrDuplicateName and a full
// category row to ErrCategoryLimit, so the handler can answer 409 for both
// instead of 500.
//
// The cap is checked in the same statement as the insert: the INSERT ...
// SELECT only produces a row while the user owns fewer than
// MaxCategoriesPerUser categories, so "no row returned" means the cap was hit
// and nothing was written. It is a ceiling against one account filling the
// table, not a data invariant: two concurrent creates by the same user at 49
// read the same snapshot and can both land. Closing that window would take a
// per-user lock across two statements, which a filter-chip limit does not
// justify.
func (s *PGStore) CreateCategory(ctx context.Context, userID, name string, sortOrder int32) (*UserCategory, error) {
	var c UserCategory
	err := s.pool.QueryRow(ctx, `
		INSERT INTO kb_user_categories (user_id, name, sort_order)
		SELECT $1::uuid, $2, $3
		WHERE (SELECT COUNT(*) FROM kb_user_categories WHERE user_id = $1::uuid) < $4
		RETURNING id::text, name, sort_order`, userID, name, sortOrder, MaxCategoriesPerUser).
		Scan(&c.ID, &c.Name, &c.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCategoryLimit
	}
	if pgxutil.IsUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, fmt.Errorf("CreateCategory: %w", err)
	}
	return &c, nil
}

// UpdateCategory renames and reorders one of this user's categories. The
// user_id predicate is what makes another user's category indistinguishable
// from a missing one.
func (s *PGStore) UpdateCategory(ctx context.Context, userID, catID, name string, sortOrder int32) (*UserCategory, error) {
	var c UserCategory
	err := s.pool.QueryRow(ctx, `
		UPDATE kb_user_categories SET name = $3, sort_order = $4
		WHERE id = $1::uuid AND user_id = $2::uuid
		RETURNING id::text, name, sort_order`, catID, userID, name, sortOrder).
		Scan(&c.ID, &c.Name, &c.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if pgxutil.IsUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateCategory: %w", err)
	}
	return &c, nil
}

// DeleteCategory removes one of this user's categories. Its assignments go
// with it through the composite FK's ON DELETE CASCADE — a deleted chip must
// stop filtering, not block the delete.
func (s *PGStore) DeleteCategory(ctx context.Context, userID, catID string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM kb_user_categories WHERE id = $1::uuid AND user_id = $2::uuid`, catID, userID)
	if err != nil {
		return fmt.Errorf("DeleteCategory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AssignCategory tags a KB with one of this user's categories. Idempotent.
//
// Ownership is NOT checked here. The composite FK (category_id, user_id) ->
// kb_user_categories (id, user_id) rejects a category belonging to somebody
// else with SQLSTATE 23503, and that rejection is translated to ErrNotFound —
// so the rule lives in the schema and holds for every writer, not just this
// one. A missing KB raises the same code via the knowledge_bases FK; both
// mean "the thing you named is not there for you", so one error suffices.
func (s *PGStore) AssignCategory(ctx context.Context, userID, catID, kbID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO kb_user_category_links (user_id, category_id, kb_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT (user_id, category_id, kb_id) DO NOTHING`, userID, catID, kbID)
	if isForeignKeyViolation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("AssignCategory: %w", err)
	}
	return nil
}

// UnassignCategory removes the tag. Idempotent — a DELETE of something that
// is already gone is the state the caller asked for.
func (s *PGStore) UnassignCategory(ctx context.Context, userID, catID, kbID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM kb_user_category_links
		WHERE user_id = $1::uuid AND category_id = $2::uuid AND kb_id = $3::uuid`,
		userID, catID, kbID)
	if err != nil {
		return fmt.Errorf("UnassignCategory: %w", err)
	}
	return nil
}

// ForgetKB deletes one user's favorite and category links for one KB. It is
// not part of Store because its callers are other packages' transactions:
// internal/kbmembers runs it inside LeaveKB and RemoveMember, once the
// membership is gone and only if the user can no longer see the KB, so the
// filters disappear atomically with the access they depended on. Whether to
// call it is the caller's decision; this function deletes unconditionally.
// The user's categories themselves stay — they are not tied to any one KB.
//
// q is the caller's transaction (or a pool); this function opens none.
func ForgetKB(ctx context.Context, q pgxutil.Querier, userID, kbID string) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM kb_favorites WHERE user_id = $1::uuid AND kb_id = $2::uuid`,
		userID, kbID); err != nil {
		return fmt.Errorf("ForgetKB: delete favorite: %w", err)
	}
	if _, err := q.Exec(ctx, `
		DELETE FROM kb_user_category_links WHERE user_id = $1::uuid AND kb_id = $2::uuid`,
		userID, kbID); err != nil {
		return fmt.Errorf("ForgetKB: delete category links: %w", err)
	}
	return nil
}

// isForeignKeyViolation reports whether err is Postgres SQLSTATE 23503.
// pgxutil has no shared helper for this code; this is the only caller that
// needs the bare code (adminkboverview also matches a constraint name).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
