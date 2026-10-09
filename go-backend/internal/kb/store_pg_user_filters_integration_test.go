//go:build integration

// Integration tests for the per-user topic filter columns (migration 0086)
// that the session-facing KB reads join in: isFavorite and userCategoryIds.
//
// Oracle: the fixture rows, written by hand with raw SQL before the query
// runs. The test knows which user starred which KB and which category it
// tagged it with because it put those rows there; the assertion compares the
// query's answer against that, not against anything the query produced. The
// second user in each test is the part that matters — these columns are
// per-caller, and a query that dropped the user_id predicate would still look
// correct for a single-user fixture.

package kb_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kb"
)

// insertUserCategory creates a category owned by userID and returns its id.
func insertUserCategory(t *testing.T, pool *pgxpool.Pool, userID, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO kb_user_categories (user_id, name) VALUES ($1::uuid, $2)
		RETURNING id::text`, userID, name).Scan(&id); err != nil {
		t.Fatalf("insert kb_user_categories %s: %v", name, err)
	}
	return id
}

// starAndTag writes userID's favorite and one category link on kbID by hand.
func starAndTag(t *testing.T, pool *pgxpool.Pool, userID, catID, kbID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO kb_favorites (user_id, kb_id) VALUES ($1::uuid, $2::uuid)`,
		userID, kbID); err != nil {
		t.Fatalf("insert kb_favorites: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO kb_user_category_links (user_id, category_id, kb_id)
		 VALUES ($1::uuid, $2::uuid, $3::uuid)`, userID, catID, kbID); err != nil {
		t.Fatalf("insert kb_user_category_links: %v", err)
	}
}

// insertPublicKB creates a public KB with the given publish/auto-subscribe
// flags and no members.
func insertPublicKB(t *testing.T, pool *pgxpool.Pool, name string, published, autoSubscribe bool) string {
	t.Helper()
	ctx := context.Background()
	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility, is_published, auto_subscribe)
		VALUES ($1, 'public', $2, $3) RETURNING id::text`, name, published, autoSubscribe).Scan(&kbID); err != nil {
		t.Fatalf("insert public KB: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})
	return kbID
}

// findRow returns the KB with id from a list result, or nil.
func findRow(list []kb.KBRow, id string) *kb.KBRow {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// assertStarred checks that row carries the caller's star and exactly catID.
func assertStarred(t *testing.T, label string, row *kb.KBRow, catID string) {
	t.Helper()
	if row == nil {
		t.Fatalf("%s: KB missing from the result", label)
	}
	if row.UserFilters == nil {
		t.Fatalf("%s: UserFilters is nil, want the caller's filters", label)
	}
	if !row.IsFavorite {
		t.Errorf("%s: isFavorite = false for the user who starred it, want true", label)
	}
	if len(row.UserCategoryIDs) != 1 || row.UserCategoryIDs[0] != catID {
		t.Errorf("%s: userCategoryIds = %v, want [%s]", label, row.UserCategoryIDs, catID)
	}
}

// assertUntouched checks that row carries filters, but none of them set —
// the view of a user who never starred or tagged the KB.
func assertUntouched(t *testing.T, label string, row *kb.KBRow) {
	t.Helper()
	if row == nil {
		t.Fatalf("%s: KB missing from the result", label)
	}
	if row.UserFilters == nil {
		t.Fatalf("%s: UserFilters is nil, want the caller's (empty) filters", label)
	}
	if row.IsFavorite {
		t.Errorf("%s: isFavorite = true for a user who never starred it — the query lost its user_id predicate", label)
	}
	if len(row.UserCategoryIDs) != 0 {
		t.Errorf("%s: userCategoryIds = %v, want empty — those are another user's categories", label, row.UserCategoryIDs)
	}
	// The SQL's COALESCE(..., ARRAY[]::text[]) is the only thing that makes
	// this non-nil: pgx scans a NULL array into a nil slice, and no Go code
	// replaces it. Dropping the COALESCE fails this line, and the JSON would
	// then read null instead of [].
	if row.UserCategoryIDs == nil {
		t.Errorf("%s: userCategoryIds is nil, want an empty slice so the JSON is [] rather than null", label)
	}
}

// TestListKnowledgeBasesWithUserFilters_PerCaller pins the join on
// GET /api/kb. Two members of the same private KB: one starred and tagged it,
// the other did neither, and the same row must report different values for
// each of them.
func TestListKnowledgeBasesWithUserFilters_PerCaller(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)

	owner := insertUser(t, pool, "kbfilters-list-owner")
	member := insertUser(t, pool, "kbfilters-list-member")

	row, err := store.CreateKnowledgeBase(ctx, "kbfilters-list-kb", nil, owner, nil)
	if err != nil {
		t.Fatalf("CreateKnowledgeBase: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, row.ID) //nolint:errcheck
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'view')`,
		row.ID, member); err != nil {
		t.Fatalf("insert kb_members: %v", err)
	}

	// Fixture: only the owner stars and tags the KB.
	catID := insertUserCategory(t, pool, owner, "kbfilters-list-cat")
	starAndTag(t, pool, owner, catID, row.ID)

	ownerList, err := store.ListKnowledgeBasesWithUserFilters(ctx, owner, 100, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBasesWithUserFilters(owner): %v", err)
	}
	assertStarred(t, "owner", findRow(ownerList, row.ID), catID)

	memberList, err := store.ListKnowledgeBasesWithUserFilters(ctx, member, 100, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBasesWithUserFilters(member): %v", err)
	}
	assertUntouched(t, "member", findRow(memberList, row.ID))
}

// TestListGlobalKnowledgeBasesWithUserFilters_BothArms pins the same join on
// GET /api/kb/global, which is a different query from GET /api/kb — and has
// two arms with their own SELECT lists. The non-admin arm is what the
// overview calls; the isAdmin=true arm (every public KB, published or not)
// is exercised per caller too, so a lost user_id predicate in either arm
// fails here. Each arm is checked on a KB only it returns: a published,
// auto-subscribed KB for the non-admin arm, and a staged one (unpublished,
// no members) that only the admin arm lists.
func TestListGlobalKnowledgeBasesWithUserFilters_BothArms(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)

	starrer := insertUser(t, pool, "kbfilters-global-starrer")
	bystander := insertUser(t, pool, "kbfilters-global-bystander")

	// Reached by the non-admin arm through auto-subscribe.
	published := insertPublicKB(t, pool, "kbfilters-global-kb", true, true)
	// Reached only by the admin arm: staged (unpublished), no members.
	staged := insertPublicKB(t, pool, "kbfilters-global-staged", false, false)

	catID := insertUserCategory(t, pool, starrer, "kbfilters-global-cat")
	starAndTag(t, pool, starrer, catID, published)
	starAndTag(t, pool, starrer, catID, staged)

	for _, arm := range []struct {
		name    string
		isAdmin bool
		kbID    string
	}{
		{"non-admin arm", false, published},
		{"admin arm", true, staged},
	} {
		mine, err := store.ListGlobalKnowledgeBasesWithUserFilters(ctx, starrer, arm.isAdmin)
		if err != nil {
			t.Fatalf("%s: ListGlobalKnowledgeBasesWithUserFilters(starrer): %v", arm.name, err)
		}
		assertStarred(t, arm.name+"/starrer", findRow(mine, arm.kbID), catID)

		theirs, err := store.ListGlobalKnowledgeBasesWithUserFilters(ctx, bystander, arm.isAdmin)
		if err != nil {
			t.Fatalf("%s: ListGlobalKnowledgeBasesWithUserFilters(bystander): %v", arm.name, err)
		}
		assertUntouched(t, arm.name+"/bystander", findRow(theirs, arm.kbID))
	}
}

// TestGetKnowledgeBase_UserFiltersArePerCaller pins the single-row read
// behind GET /api/kb/{id} and the PATCH re-read: the same KB, two callers,
// two answers.
func TestGetKnowledgeBase_UserFiltersArePerCaller(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)

	starrer := insertUser(t, pool, "kbfilters-get-starrer")
	bystander := insertUser(t, pool, "kbfilters-get-bystander")
	kbID := insertPublicKB(t, pool, "kbfilters-get-kb", true, false)

	catID := insertUserCategory(t, pool, starrer, "kbfilters-get-cat")
	starAndTag(t, pool, starrer, catID, kbID)

	mine, err := store.GetKnowledgeBase(ctx, kbID, starrer)
	if err != nil {
		t.Fatalf("GetKnowledgeBase(starrer): %v", err)
	}
	assertStarred(t, "starrer", mine, catID)

	theirs, err := store.GetKnowledgeBase(ctx, kbID, bystander)
	if err != nil {
		t.Fatalf("GetKnowledgeBase(bystander): %v", err)
	}
	assertUntouched(t, "bystander", theirs)
}

// TestAPIKeyListingsCarryNoUserFilters pins that the plain list methods — the
// ones GET /api/v1/kb, /openai/v1/models and the KB router call — return no
// per-user state, even for a caller who starred and tagged every KB in the
// result. The JSON check is the contract an integration sees: neither key may
// appear, not even as false or null.
func TestAPIKeyListingsCarryNoUserFilters(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)

	user := insertUser(t, pool, "kbfilters-apikey-user")
	private, err := store.CreateKnowledgeBase(ctx, "kbfilters-apikey-private", nil, user, nil)
	if err != nil {
		t.Fatalf("CreateKnowledgeBase: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, private.ID) //nolint:errcheck
	})
	public := insertPublicKB(t, pool, "kbfilters-apikey-public", true, true)

	catID := insertUserCategory(t, pool, user, "kbfilters-apikey-cat")
	starAndTag(t, pool, user, catID, private.ID)
	starAndTag(t, pool, user, catID, public)

	check := func(label string, rows []kb.KBRow, kbID string) {
		t.Helper()
		row := findRow(rows, kbID)
		if row == nil {
			t.Fatalf("%s: KB %s missing from the result", label, kbID)
		}
		if row.UserFilters != nil {
			t.Errorf("%s: UserFilters = %+v, want nil on an API-key listing", label, *row.UserFilters)
		}
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", label, err)
		}
		for _, k := range []string{"isFavorite", "userCategoryIds"} {
			if v, ok := m[k]; ok {
				t.Errorf("%s: JSON carries %q = %s, want the key absent", label, k, v)
			}
		}
	}

	personal, err := store.ListKnowledgeBases(ctx, user, 100, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBases: %v", err)
	}
	check("ListKnowledgeBases", personal, private.ID)

	for _, isAdmin := range []bool{false, true} {
		globals, err := store.ListGlobalKnowledgeBases(ctx, user, isAdmin)
		if err != nil {
			t.Fatalf("ListGlobalKnowledgeBases(isAdmin=%v): %v", isAdmin, err)
		}
		label := "ListGlobalKnowledgeBases(isAdmin=false)"
		if isAdmin {
			label = "ListGlobalKnowledgeBases(isAdmin=true)"
		}
		check(label, globals, public)
	}
}
