package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// allowFileScheme gates file:// URL acceptance. False by default so production
// never allows local-file access. Tests set it true via init().
var allowFileScheme bool

// safeTransportInstalled is set to true inside installOnce.Do when a caller
// has registered an SSRF-safe transport. Used by requireSafeTransport.
var safeTransportInstalled bool

// CloneOptions configures CloneAndCollect.
type CloneOptions struct {
	URL       string
	Branch    string            // "" => default HEAD
	Token     string            // "" => public
	Transport http.RoundTripper // SSRF-safe; nil => go-git default (tests/local file://)
}

// RepoFile is a file from the cloned repo that passed ShouldIngest.
type RepoFile struct {
	Path     string
	BlobSHA  string
	Size     int
	MimeType string
	Content  []byte
}

// CloneResult holds the HEAD commit SHA and the collected files.
type CloneResult struct {
	CommitSHA string
	Files     []RepoFile
	// Truncated is true when the file list was capped at GitRepoMaxFiles.
	Truncated bool
}

var installOnce sync.Once

// InstallSafeGitTransport routes go-git's https traffic through rt. Process-wide
// (go-git's protocol registry is global); idempotent via sync.Once.
func InstallSafeGitTransport(rt http.RoundTripper) {
	if rt == nil {
		return
	}
	installOnce.Do(func() {
		c := &http.Client{Transport: rt}
		client.InstallProtocol("https", githttp.NewClient(c))
		safeTransportInstalled = true
	})
}

// requireSafeTransport returns an error when an https clone would run without
// an SSRF-safe transport (fail closed). file:// (tests) is exempt.
//
// The SSRF transport's safeDialContext re-resolves the host and rejects
// loopback/RFC1918/169.254.0.0/16 on EVERY dial, including redirect hops.
// This satisfies the per-hop re-check requirement; the global install via
// InstallSafeGitTransport is acceptable because it is called once at startup
// before any user-supplied URL is processed.
func requireSafeTransport(scheme string, transportProvided, installed bool) error {
	if scheme == "https" && !transportProvided && !installed {
		return errors.New("gitrepo: refusing https clone without an SSRF-safe transport")
	}
	return nil
}

// CloneAndCollect shallow-clones the repo to a temp dir, walks the HEAD tree,
// applies ShouldIngest, and returns the surviving files with content.
func CloneAndCollect(ctx context.Context, opts CloneOptions) (*CloneResult, error) {
	if opts.Transport != nil {
		InstallSafeGitTransport(opts.Transport)
	}
	if err := validateRepoURL(opts.URL); err != nil {
		return nil, err
	}

	u, _ := url.Parse(opts.URL) // already validated; ignore error
	if err := requireSafeTransport(u.Scheme, opts.Transport != nil, safeTransportInstalled); err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "gitrepo-*")
	if err != nil {
		return nil, fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	co := &git.CloneOptions{
		URL:          opts.URL,
		Depth:        1,
		SingleBranch: true,
		Tags:         git.NoTags,
	}
	if opts.Branch != "" {
		co.ReferenceName = plumbing.NewBranchReferenceName(opts.Branch)
	}
	if opts.Token != "" {
		co.Auth = &githttp.BasicAuth{Username: "git", Password: opts.Token}
	}

	repo, err := git.PlainCloneContext(ctx, tmp, false, co)
	if err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("head: %w", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}

	var files []RepoFile
	var truncated bool
	walkErr := tree.Files().ForEach(func(f *object.File) error {
		if len(files) >= GitRepoMaxFiles {
			truncated = true
			return storer.ErrStop
		}
		// Reject paths that could escape the storage prefix. The storage key
		// is derived from the blob SHA (see sync.go), so attacker-controlled
		// path bytes never reach the storage layer — this is defense in depth
		// to keep traversal/absolute paths out of the DB and pipeline entirely.
		if path.IsAbs(f.Name) || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "..") {
			return nil
		}
		// Path/size filter first — avoids reading huge or excluded blobs into memory.
		ok, mime := ShouldIngestPath(f.Name, int(f.Size))
		if !ok {
			return nil
		}
		content, rerr := f.Contents()
		if rerr != nil {
			return nil // skip unreadable blob, don't fail the whole sync
		}
		cb := []byte(content)
		// Binary content sniff on survivors only.
		if IsBinaryContent(cb) {
			return nil
		}
		files = append(files, RepoFile{
			Path:     f.Name,
			BlobSHA:  f.Hash.String(),
			Size:     int(f.Size),
			MimeType: mime,
			Content:  cb,
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk tree: %w", walkErr)
	}
	return &CloneResult{CommitSHA: head.Hash().String(), Files: files, Truncated: truncated}, nil
}

// validateRepoURL enforces https always; file:// only when allowFileScheme is
// true (tests); rejects userinfo to prevent host-confusion attacks.
func validateRepoURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse repo url: %w", err)
	}
	if u.User != nil {
		return fmt.Errorf("unsupported repo url: userinfo not allowed")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "file":
		if allowFileScheme {
			return nil
		}
		return fmt.Errorf("unsupported repo url scheme %q (file:// is not allowed in production)", u.Scheme)
	default:
		return fmt.Errorf("unsupported repo url scheme %q (https only)", u.Scheme)
	}
}
