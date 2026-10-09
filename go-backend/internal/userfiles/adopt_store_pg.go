package userfiles

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGAdoptStore is the Postgres-backed AdoptStore.
type PGAdoptStore struct{ pool *pgxpool.Pool }

func NewAdoptStore(pool *pgxpool.Pool) *PGAdoptStore { return &PGAdoptStore{pool: pool} }

var _ AdoptStore = (*PGAdoptStore)(nil)

func (s *PGAdoptStore) GetLegacy(ctx context.Context, fileID, kbID string) (*LegacyFile, error) {
	if !validID(fileID) {
		return nil, nil
	}
	var f LegacyFile
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, kb_id::text, name, COALESCE(type,''), COALESCE(size,0)::bigint,
		       COALESCE(origin,''), COALESCE(status,''), COALESCE(storage_path,''),
		       COALESCE(user_file_id::text,''), COALESCE(uploaded_by::text,'')
		  FROM files WHERE id = $1::uuid AND kb_id = $2::uuid`, fileID, kbID).
		Scan(&f.ID, &f.KBID, &f.Name, &f.Type, &f.Size, &f.Origin, &f.Status, &f.StoragePath, &f.UserFileID, &f.UploadedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *PGAdoptStore) UserExists(ctx context.Context, userID string) (bool, error) {
	if !validID(userID) {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid)`, userID).Scan(&ok)
	return ok, err
}

func (s *PGAdoptStore) FindBySHA(ctx context.Context, ownerID, sha string) (*UserFile, error) {
	f, err := scanFile(s.pool.QueryRow(ctx,
		`SELECT `+fileCols+` FROM user_files WHERE owner_user_id = $1::uuid AND sha256 = $2`, ownerID, sha))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

func (s *PGAdoptStore) LinkFile(ctx context.Context, fileID, userFileID, storagePath, uploaderID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE files SET user_file_id = $2::uuid, storage_path = $3,
		                 uploaded_by = COALESCE(uploaded_by, $4::uuid)
		 WHERE id = $1::uuid AND user_file_id IS NULL
		   AND COALESCE(status,'') NOT IN ('pending','processing')`, fileID, userFileID, storagePath, uploaderID)
	if err != nil {
		if pgxutil.IsUniqueViolation(err) {
			return false, nil // this KB already holds a copy of that library file
		}
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PGAdoptStore) CountByStoragePath(ctx context.Context, storagePath string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM files WHERE storage_path = $1`, storagePath).Scan(&n)
	return n, err
}

func (s *PGAdoptStore) DeleteUnreferenced(ctx context.Context, userFileID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_files u WHERE u.id = $1::uuid
		   AND NOT EXISTS (SELECT 1 FROM files f WHERE f.user_file_id = u.id)`, userFileID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
