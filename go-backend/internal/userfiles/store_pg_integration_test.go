//go:build integration

package userfiles_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/userfiles"
)

func newFile(owner, sha string, size int64) userfiles.NewUserFile {
	id := uuid.NewString()
	return userfiles.NewUserFile{
		ID: id, OwnerUserID: owner, Name: "f-" + sha[:4] + ".pdf", Mime: "application/pdf",
		Size: size, SHA256: sha, StoragePath: "users/" + owner + "/" + id,
	}
}

func sha(c string) string {
	s := ""
	for len(s) < 64 {
		s += c
	}
	return s[:64]
}

func mustInsert(t *testing.T, st *userfiles.PGStore, f userfiles.NewUserFile) *userfiles.UserFile {
	t.Helper()
	row, _, err := st.Insert(context.Background(), f)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	p := openMainPool(t)
	t.Cleanup(func() {
		p.Exec(context.Background(), `DELETE FROM files WHERE user_file_id = $1::uuid`, row.ID) //nolint:errcheck
		p.Exec(context.Background(), `DELETE FROM user_files WHERE id = $1::uuid`, row.ID)      //nolint:errcheck
	})
	return row
}

func seedKBNamed(t *testing.T, pool *pgxpool.Pool, name, vis string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO knowledge_bases (name, visibility) VALUES ($1, $2) RETURNING id::text`, name, vis).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, id) }) //nolint:errcheck
	return id
}

func addCopy(t *testing.T, pool *pgxpool.Pool, kbID, ufID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO files (kb_id, name, type, status, storage_path, user_file_id)
		VALUES ($1::uuid, 'a.pdf', 'application/pdf', 'completed', 'p', $2::uuid) RETURNING id::text`,
		kbID, ufID).Scan(&id); err != nil {
		t.Fatalf("insert copy: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM files WHERE id=$1::uuid`, id) }) //nolint:errcheck
	return id
}

func TestStoreInsert(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)

	first := mustInsert(t, st, newFile(o1, sha("a"), 5))
	again, created, err := st.Insert(ctx, newFile(o1, sha("a"), 5))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("dup: created=%v row=%v want %v err=%v", created, again, first.ID, err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM user_files WHERE owner_user_id=$1::uuid`, o1).Scan(&n) //nolint:errcheck
	if n != 1 {
		t.Fatalf("rows=%d want 1", n)
	}
	other, created, err := st.Insert(ctx, newFile(o2, sha("a"), 5))
	if err != nil || !created {
		t.Fatalf("other owner: created=%v err=%v", created, err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM user_files WHERE id=$1::uuid`, other.ID) }) //nolint:errcheck
	if first.KBs == nil || len(first.KBs) != 0 {
		t.Fatalf("KBs must be non-nil empty, got %#v", first.KBs)
	}
	// A dedup hit reports the KBs the file is already in.
	kb := seedKBNamed(t, pool, "dedup-kb", "private")
	copyID := addCopy(t, pool, kb, first.ID)
	linked, created, err := st.Insert(ctx, newFile(o1, sha("a"), 5))
	if err != nil || created || len(linked.KBs) != 1 || linked.KBs[0].FileID != copyID {
		t.Fatalf("dedup KBs: created=%v kbs=%#v err=%v", created, linked.KBs, err)
	}
}

func TestStoreGet(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)
	f := mustInsert(t, st, newFile(o1, sha("b"), 5))

	got, err := st.Get(ctx, o1, f.ID)
	if err != nil || got.KBs == nil || len(got.KBs) != 0 {
		t.Fatalf("Get own: %#v %v", got, err)
	}
	for _, c := range []struct{ owner, id string }{{o2, f.ID}, {o1, uuid.NewString()}, {o1, "not-a-uuid"}} {
		if _, err := st.Get(ctx, c.owner, c.id); !errors.Is(err, userfiles.ErrNotFound) {
			t.Fatalf("Get(%q) err=%v want ErrNotFound", c.id, err)
		}
	}
}

func TestStoreList(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o := seedUser(t, pool)
	a := mustInsert(t, st, newFile(o, sha("1"), 1))
	b := mustInsert(t, st, newFile(o, sha("2"), 2))
	c := mustInsert(t, st, newFile(o, sha("3"), 3))
	kb := seedKBNamed(t, pool, "dedup-kb", "private")
	fid := addCopy(t, pool, kb, a.ID)

	items, total, err := st.List(ctx, o, 10, 0)
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("List: %d/%d %v", len(items), total, err)
	}
	if items[0].ID != c.ID || items[1].ID != b.ID || items[2].ID != a.ID {
		t.Fatalf("order wrong: %v %v %v", items[0].ID, items[1].ID, items[2].ID)
	}
	if len(items[0].KBs) != 0 || items[0].KBs == nil {
		t.Fatalf("KBs nil/non-empty: %#v", items[0].KBs)
	}
	if len(items[2].KBs) != 1 || items[2].KBs[0] != (userfiles.KBLink{KBID: kb, FileID: fid, Status: "completed"}) {
		t.Fatalf("KBs: %#v", items[2].KBs)
	}
	page, total, err := st.List(ctx, o, 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].ID != b.ID {
		t.Fatalf("page: %#v %d %v", page, total, err)
	}
	empty, total, err := st.List(ctx, seedUser(t, pool), 10, 0)
	if err != nil || total != 0 || empty == nil || len(empty) != 0 {
		t.Fatalf("empty: %#v %d %v", empty, total, err)
	}
}

func TestStoreRename(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)
	f := mustInsert(t, st, newFile(o1, sha("c"), 5))

	got, err := st.Rename(ctx, o1, f.ID, "  neu.pdf ")
	if err != nil || got.Name != "neu.pdf" {
		t.Fatalf("Rename: %#v %v", got, err)
	}
	if got, err := st.Rename(ctx, o1, f.ID, "Andere.PDF"); err != nil || got.Name != "Andere.PDF" {
		t.Fatalf("case-only ext change must pass: %#v %v", got, err)
	}
	if _, err := st.Rename(ctx, o2, f.ID, "x"); !errors.Is(err, userfiles.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}
	if _, err := st.Rename(ctx, o1, "nope", "x"); !errors.Is(err, userfiles.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	for _, n := range []string{"", "   ", fmt.Sprintf("%0256d", 1),
		"neu.csv", "neu.PDF.svg", "neu", "neu.exe", "neu.html", "neu.docx"} {
		if _, err := st.Rename(ctx, o1, f.ID, n); !errors.Is(err, userfiles.ErrInvalidName) {
			t.Fatalf("name %q: %v", n, err)
		}
	}
}

func TestStoreUsage(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)
	f := mustInsert(t, st, newFile(o1, sha("d"), 5))

	none, err := st.Usage(ctx, o1, f.ID)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no copies: %#v %v", none, err)
	}
	kbA := seedKBNamed(t, pool, "a-kb", "private")
	kbB := seedKBNamed(t, pool, "b-kb", "public")
	if _, err := pool.Exec(ctx, `INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid,$2::uuid,'admin')`, kbA, o1); err != nil {
		t.Fatalf("member: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM kb_members WHERE kb_id=$1::uuid`, kbA) }) //nolint:errcheck
	addCopy(t, pool, kbA, f.ID)
	addCopy(t, pool, kbB, f.ID)

	rows, err := st.Usage(ctx, o1, f.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("usage: %#v %v", rows, err)
	}
	if rows[0].ID != kbA || rows[0].Name != "a-kb" || rows[0].Visibility != "private" || rows[0].MemberCount != 1 {
		t.Fatalf("row0: %#v", rows[0])
	}
	if rows[1].ID != kbB || rows[1].Visibility != "public" || rows[1].MemberCount != 0 {
		t.Fatalf("row1: %#v", rows[1])
	}
	if _, err := st.Usage(ctx, o2, f.ID); !errors.Is(err, userfiles.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}
	if _, err := st.Usage(ctx, o1, "junk"); !errors.Is(err, userfiles.ErrNotFound) {
		t.Fatalf("junk id: %v", err)
	}
}

func TestStoreUsedBytes(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)
	if n, err := st.UsedBytes(ctx, o1); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	a := mustInsert(t, st, newFile(o1, sha("e"), 100))
	mustInsert(t, st, newFile(o1, sha("f"), 20))
	mustInsert(t, st, newFile(o2, sha("e"), 999))
	addCopy(t, pool, seedKB(t, pool), a.ID)
	addCopy(t, pool, seedKB(t, pool), a.ID)
	if n, err := st.UsedBytes(ctx, o1); err != nil || n != 120 {
		t.Fatalf("used=%d err=%v want 120", n, err)
	}
}

func TestStoreQuotaOverride(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o := seedUser(t, pool)
	if q, err := st.QuotaOverride(ctx, o); err != nil || q != nil {
		t.Fatalf("default: %v %v", q, err)
	}
	pool.Exec(ctx, `UPDATE users SET file_quota_bytes=10 WHERE id=$1::uuid`, o) //nolint:errcheck
	if q, err := st.QuotaOverride(ctx, o); err != nil || q == nil || *q != 10 {
		t.Fatalf("set: %v %v", q, err)
	}
}

func TestStoreListIDsByOwner(t *testing.T) {
	pool := openMainPool(t)
	st := userfiles.NewStore(pool)
	ctx := context.Background()
	o1, o2 := seedUser(t, pool), seedUser(t, pool)
	a := mustInsert(t, st, newFile(o1, sha("7"), 1))
	b := mustInsert(t, st, newFile(o1, sha("8"), 1))
	mustInsert(t, st, newFile(o2, sha("9"), 1))
	ids, err := st.ListIDsByOwner(ctx, o1)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids: %v %v", ids, err)
	}
	if !((ids[0] == a.ID && ids[1] == b.ID) || (ids[0] == b.ID && ids[1] == a.ID)) {
		t.Fatalf("ids mismatch: %v", ids)
	}
	if ids, err := st.ListIDsByOwner(ctx, seedUser(t, pool)); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("empty: %#v %v", ids, err)
	}
}
