package userfiles

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/uploadcheck"
)

// ErrNotFound means the library file does not exist for this owner. Another
// owner's file and a malformed id are deliberately indistinguishable from a
// missing one.
var ErrNotFound = errors.New("user file not found")

// ErrInvalidName means a rename target is empty or longer than 255 bytes.
var ErrInvalidName = errors.New("invalid file name")

const maxNameBytes = 255

// UserFile is one library file.
type UserFile struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"-"`
	Name        string    `json:"name"`
	Mime        string    `json:"mime"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"-"`
	StoragePath string    `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	KBs         []KBLink  `json:"kbs"` // always non-nil ([]), filled by Get/List
}

// KBLink is one KB copy of a library file.
type KBLink struct {
	KBID   string `json:"kbId"`
	FileID string `json:"fileId"`
	Status string `json:"status"`
}

// KBUsage is one row of the usage listing (impact preview for delete dialogs).
type KBUsage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Visibility  string `json:"visibility"`
	MemberCount int    `json:"memberCount"`
}

// NewUserFile is the insert payload.
type NewUserFile struct {
	ID          string // caller-generated (uuid.New) so the blob key is known before insert
	OwnerUserID string
	Name, Mime  string
	Size        int64
	SHA256      string
	StoragePath string
}

// Store is the library data layer. PGStore is its only implementation.
type Store interface {
	// Insert inserts f. If (owner, sha256) already exists it inserts nothing
	// and returns the EXISTING row with created=false.
	Insert(ctx context.Context, f NewUserFile) (row *UserFile, created bool, err error)
	// Get returns the owner's file (ErrNotFound for a missing id OR another owner's id).
	Get(ctx context.Context, ownerID, id string) (*UserFile, error)
	List(ctx context.Context, ownerID string, limit, offset int) ([]UserFile, int, error)
	Rename(ctx context.Context, ownerID, id, name string) (*UserFile, error)
	Usage(ctx context.Context, ownerID, id string) ([]KBUsage, error)
	UsedBytes(ctx context.Context, ownerID string) (int64, error)
	// QuotaOverride returns users.file_quota_bytes (nil = not set).
	QuotaOverride(ctx context.Context, ownerID string) (*int64, error)
	// ListIDsByOwner returns every user_files id of ownerID (user-delete fan-out).
	ListIDsByOwner(ctx context.Context, ownerID string) ([]string, error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main pool.
func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

var _ Store = (*PGStore)(nil)

const fileCols = `id::text, owner_user_id::text, name, mime, size, sha256::text, storage_path, created_at`

func scanFile(row pgx.Row) (*UserFile, error) {
	var f UserFile
	if err := row.Scan(&f.ID, &f.OwnerUserID, &f.Name, &f.Mime, &f.Size, &f.SHA256, &f.StoragePath, &f.CreatedAt); err != nil {
		return nil, err
	}
	f.KBs = []KBLink{}
	return &f, nil
}

// validID reports whether s parses as a uuid; malformed ids map to ErrNotFound
// instead of a SQL cast error.
func validID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func (s *PGStore) Insert(ctx context.Context, f NewUserFile) (*UserFile, bool, error) {
	row, err := scanFile(s.pool.QueryRow(ctx, `
		INSERT INTO user_files (id, owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
		ON CONFLICT ON CONSTRAINT user_files_owner_sha256_key DO NOTHING
		RETURNING `+fileCols,
		f.ID, f.OwnerUserID, f.Name, f.Mime, f.Size, f.SHA256, f.StoragePath))
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	row, err = scanFile(s.pool.QueryRow(ctx,
		`SELECT `+fileCols+` FROM user_files WHERE owner_user_id = $1::uuid AND sha256 = $2`,
		f.OwnerUserID, f.SHA256))
	if err != nil {
		return nil, false, err
	}
	// The dedup hit may already be linked into KBs; report them like Get does.
	row, err = s.Get(ctx, f.OwnerUserID, row.ID)
	if err != nil {
		return nil, false, err
	}
	return row, false, nil
}

func (s *PGStore) Get(ctx context.Context, ownerID, id string) (*UserFile, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	f, err := scanFile(s.pool.QueryRow(ctx,
		`SELECT `+fileCols+` FROM user_files WHERE id = $1::uuid AND owner_user_id = $2::uuid`, id, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.fillKBs(ctx, []UserFile{*f}, func(i int, l KBLink) { f.KBs = append(f.KBs, l) }); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *PGStore) List(ctx context.Context, ownerID string, limit, offset int) ([]UserFile, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM user_files WHERE owner_user_id = $1::uuid`, ownerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+fileCols+` FROM user_files
		WHERE owner_user_id = $1::uuid ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	items := []UserFile{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		items = append(items, *f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.fillKBs(ctx, items, func(i int, l KBLink) { items[i].KBs = append(items[i].KBs, l) }); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// fillKBs loads the KB copies of files with one query and calls add(index, link)
// for each, in creation order.
func (s *PGStore) fillKBs(ctx context.Context, files []UserFile, add func(i int, l KBLink)) error {
	if len(files) == 0 {
		return nil
	}
	ids := make([]string, len(files))
	idx := make(map[string]int, len(files))
	for i, f := range files {
		ids[i] = f.ID
		idx[f.ID] = i
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_file_id::text, kb_id::text, id::text, status::text
		  FROM files WHERE user_file_id = ANY($1::uuid[]) ORDER BY created_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ufID string
		var l KBLink
		if err := rows.Scan(&ufID, &l.KBID, &l.FileID, &l.Status); err != nil {
			return err
		}
		add(idx[ufID], l)
	}
	return rows.Err()
}

func (s *PGStore) Rename(ctx context.Context, ownerID, id, name string) (*UserFile, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameBytes {
		return nil, ErrInvalidName
	}
	// The extension picks the parser (and the spreadsheet size gate) when the
	// file is added to a KB, so a rename must not change it and must still
	// pass the upload filename rules.
	var oldName string
	if err := s.pool.QueryRow(ctx,
		`SELECT name FROM user_files WHERE id = $1::uuid AND owner_user_id = $2::uuid`,
		id, ownerID).Scan(&oldName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(name), filepath.Ext(oldName)) || uploadcheck.ValidateName(name) != nil {
		return nil, ErrInvalidName
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_files SET name = $3, updated_at = now() WHERE id = $1::uuid AND owner_user_id = $2::uuid`,
		id, ownerID, name)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, ownerID, id)
}

type usageRow struct {
	ID          string `db:"id"`
	Name        string `db:"name"`
	Visibility  string `db:"visibility"`
	MemberCount int    `db:"member_count"`
}

func (s *PGStore) Usage(ctx context.Context, ownerID, id string) ([]KBUsage, error) {
	if _, err := s.Get(ctx, ownerID, id); err != nil {
		return nil, err
	}
	rows, err := pgxutil.QueryRows[usageRow](ctx, s.pool, `
		SELECT kb.id::text AS id, kb.name AS name, kb.visibility::text AS visibility,
		       (SELECT COUNT(*)::int FROM kb_members m WHERE m.kb_id = kb.id) AS member_count
		  FROM files f
		  JOIN user_files uf ON uf.id = f.user_file_id
		  JOIN knowledge_bases kb ON kb.id = f.kb_id
		 WHERE uf.id = $2::uuid AND uf.owner_user_id = $1::uuid
		 ORDER BY kb.name`, ownerID, id)
	if err != nil {
		return nil, err
	}
	out := make([]KBUsage, len(rows))
	for i, r := range rows {
		out[i] = KBUsage(r)
	}
	return out, nil
}

func (s *PGStore) UsedBytes(ctx context.Context, ownerID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(size), 0)::bigint FROM user_files WHERE owner_user_id = $1::uuid`, ownerID).Scan(&n)
	return n, err
}

func (s *PGStore) QuotaOverride(ctx context.Context, ownerID string) (*int64, error) {
	var q *int64
	err := s.pool.QueryRow(ctx, `SELECT file_quota_bytes FROM users WHERE id = $1::uuid`, ownerID).Scan(&q)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (s *PGStore) ListIDsByOwner(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM user_files WHERE owner_user_id = $1::uuid ORDER BY created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
