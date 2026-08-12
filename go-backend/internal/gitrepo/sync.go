package gitrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/storage"
)

// ChunkDeleter abstracts deleting all vector chunks for a single file across
// every dimension table. Satisfied by *vector.ChunkService.
type ChunkDeleter interface {
	DeleteChunksByFileIDAllDims(ctx context.Context, fileID string) error
}

// SyncDeps holds the external dependencies needed by the git-repo sync handler.
type SyncDeps struct {
	Store        Store
	JWTSecret    string
	AsynqClient  *asynq.Client
	Storage      storage.Storage
	ChunkDeleter ChunkDeleter
	Transport    http.RoundTripper // SSRF-safe; nil in tests
}

// reconcile diffs existing DB rows against the freshly-walked desired set,
// keyed on path. Same path + same blob sha => skip. Changed => delete old +
// create. New => create. Missing from desired => delete.
// reconcile is a pure function with no I/O.
func reconcile(existing []GitRepoFileRow, desired []RepoFile) (toCreate []RepoFile, toDelete []GitRepoFileRow) {
	byPath := make(map[string]GitRepoFileRow, len(existing))
	for _, e := range existing {
		byPath[e.Path] = e
	}
	desiredPaths := make(map[string]bool, len(desired))
	for _, d := range desired {
		desiredPaths[d.Path] = true
		ex, ok := byPath[d.Path]
		if !ok {
			toCreate = append(toCreate, d)
			continue
		}
		if ex.BlobSHA != d.BlobSHA {
			toCreate = append(toCreate, d)
			toDelete = append(toDelete, ex)
		}
	}
	for _, e := range existing {
		if !desiredPaths[e.Path] {
			toDelete = append(toDelete, e)
		}
	}
	return toCreate, toDelete
}

// NewSyncHandler returns an asynq handler that performs a full delta-reconcile
// sync for a single Git repository source.
func NewSyncHandler(deps SyncDeps) func(ctx context.Context, task *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.GitRepoSyncPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal git repo sync payload: %w", err)
		}
		slog.Info("git repo sync started", "sourceId", payload.SourceID)
		if err := syncGitRepoSource(ctx, deps, payload.SourceID); err != nil {
			slog.Error("git repo sync failed", "sourceId", payload.SourceID, "error", sanitize(err.Error()))
			return err
		}
		slog.Info("git repo sync completed", "sourceId", payload.SourceID)
		return nil
	}
}

func syncGitRepoSource(ctx context.Context, deps SyncDeps, sourceID string) error {
	src, err := deps.Store.GetGitRepoSourceByID(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if src == nil {
		slog.Warn("git repo source not found, skipping", "sourceId", sourceID)
		return nil
	}
	if src.Status == "paused" {
		slog.Debug("git repo source paused, skipping", "sourceId", sourceID)
		return nil
	}

	syncing := "syncing"
	_ = deps.Store.SetGitRepoSyncState(ctx, sourceID, SyncState{Status: syncing})

	token := ""
	if src.IsPrivate && src.AccessTokenEncrypted != nil && *src.AccessTokenEncrypted != "" {
		token, err = confluence.DecryptToken(*src.AccessTokenEncrypted, deps.JWTSecret)
		if err != nil {
			return failSync(ctx, deps, sourceID, fmt.Errorf("decrypt token: %w", err))
		}
	}
	branch := ""
	if src.Branch != nil {
		branch = *src.Branch
	}

	cloneCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	res, err := CloneAndCollect(cloneCtx, CloneOptions{
		URL: src.RepoURL, Branch: branch, Token: token, Transport: deps.Transport,
	})
	if err != nil {
		return failSync(ctx, deps, sourceID, err)
	}
	if res.Truncated {
		slog.Warn("git repo file list truncated", "sourceId", sourceID, "limit", GitRepoMaxFiles)
	}

	// No-op if HEAD unchanged since last successful sync.
	if src.LastCommitSHA != nil && *src.LastCommitSHA == res.CommitSHA {
		now := time.Now()
		active := "active"
		return deps.Store.SetGitRepoSyncState(ctx, sourceID, SyncState{
			Status: active, LastSyncedAt: &now,
		})
	}

	existing, err := deps.Store.ListGitRepoFiles(ctx, sourceID)
	if err != nil {
		return failSync(ctx, deps, sourceID, fmt.Errorf("list existing: %w", err))
	}
	toCreate, toDelete := reconcile(existing, res.Files)

	// Delete removed/changed files: chunks + storage + DB row (tolerant of individual errors).
	for _, d := range toDelete {
		if deps.ChunkDeleter != nil {
			if cerr := deps.ChunkDeleter.DeleteChunksByFileIDAllDims(ctx, d.FileID); cerr != nil {
				slog.Warn("delete chunks failed", "fileId", d.FileID, "error", cerr)
			}
		}
		if d.StoragePath != "" {
			if serr := deps.Storage.DeleteFile(ctx, d.StoragePath); serr != nil {
				slog.Warn("delete storage file failed", "path", d.StoragePath, "error", serr)
			}
		}
		if derr := deps.Store.DeleteGitRepoFileByID(ctx, d.FileID); derr != nil {
			slog.Warn("delete db file row failed", "fileId", d.FileID, "error", derr)
		}
	}

	// Create + enqueue ingest for new/changed files.
	created := 0
	for _, f := range toCreate {
		// Storage key is derived from the (trusted, hex) blob SHA, never the
		// repo-relative path — a crafted repo could otherwise use a path like
		// "../../x" to escape the gitrepo/{sourceID}/ prefix (path traversal).
		// The repo path is preserved separately as the display name / GitFilePath,
		// which never forms a storage key. CloneAndCollect also rejects absolute
		// and ".." paths as defense in depth.
		pathHash := func() string { h := sha256.Sum256([]byte(f.Path)); return hex.EncodeToString(h[:])[:16] }()
		storagePath := fmt.Sprintf("gitrepo/%s/%s-%s", sourceID, f.BlobSHA, pathHash)
		if err := deps.Storage.StoreFile(ctx, storagePath, f.Content, f.MimeType); err != nil {
			slog.Warn("store git file failed", "path", f.Path, "error", err)
			continue
		}
		fileID, err := deps.Store.CreateGitRepoFile(ctx, CreateGitRepoFileInput{
			KbID: src.KbID, Name: f.Path, Type: f.MimeType, Size: f.Size,
			StoragePath: storagePath, GitRepoSourceID: sourceID,
			GitFilePath: f.Path, GitBlobSHA: f.BlobSHA,
		})
		if err != nil {
			slog.Warn("create git file row failed", "path", f.Path, "error", err)
			continue
		}
		jb, _ := json.Marshal(jobs.FileProcessingPayload{
			FileID: fileID, KbID: src.KbID, FilePath: storagePath,
			OriginalName: path.Base(f.Path), MimeType: f.MimeType,
		})
		if _, err := deps.AsynqClient.Enqueue(
			asynq.NewTask(jobs.TypeFileProcessing, jb),
			asynq.Queue(jobs.QueueQuick), asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
		); err != nil {
			slog.Warn("enqueue git file processing failed", "fileId", fileID, "error", err)
		} else {
			created++
		}
	}

	now := time.Now()
	active := "active"
	total := len(res.Files)
	zeroFail := 0
	return deps.Store.SetGitRepoSyncState(ctx, sourceID, SyncState{
		Status: active, LastCommitSHA: &res.CommitSHA, LastSyncedAt: &now,
		FileCount: &total, SyncTotal: &created, SyncProgress: intPtr(0),
		ConsecutiveFailures: &zeroFail,
	})
}

func failSync(ctx context.Context, deps SyncDeps, sourceID string, cause error) error {
	msg := sanitize(cause.Error())
	errStatus := "error"
	src, _ := deps.Store.GetGitRepoSourceByID(ctx, sourceID)
	fails := 1
	if src != nil {
		fails = src.ConsecutiveFailures + 1
	}
	_ = deps.Store.SetGitRepoSyncState(ctx, sourceID, SyncState{
		Status: errStatus, ErrorMessage: &msg, ConsecutiveFailures: &fails,
	})
	return cause
}

func intPtr(i int) *int { return &i }

// sanitize strips a token if it ever leaked into a go-git error string.
func sanitize(s string) string {
	if i := strings.Index(s, "@"); i >= 0 {
		if j := strings.Index(s, "://"); j >= 0 && j < i {
			return s[:j+3] + "***" + s[i:]
		}
	}
	return s
}
