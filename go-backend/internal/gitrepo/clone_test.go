package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Enable file:// for this test binary only; production never sets this.
func init() { allowFileScheme = true }

func initFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# fixture")
	write("main.go", "package main")
	write("node_modules/x/index.js", "skip me")
	write("logo.png", "\x89PNG\x00binary")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestCloneAndCollect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for fixture creation")
	}
	dir := initFixtureRepo(t)
	res, err := CloneAndCollect(context.Background(), CloneOptions{URL: "file://" + dir})
	if err != nil {
		t.Fatalf("CloneAndCollect: %v", err)
	}
	if res.CommitSHA == "" {
		t.Fatal("expected a commit sha")
	}
	got := map[string]bool{}
	for _, f := range res.Files {
		got[f.Path] = true
	}
	if !got["README.md"] || !got["main.go"] {
		t.Fatalf("expected README.md and main.go, got %v", got)
	}
	if got["node_modules/x/index.js"] || got["logo.png"] {
		t.Fatalf("filter leaked skipped files: %v", got)
	}
}

func TestValidateRepoURL(t *testing.T) {
	// Save and restore allowFileScheme so sub-tests are isolated.
	orig := allowFileScheme
	defer func() { allowFileScheme = orig }()

	cases := []struct {
		name      string
		url       string
		fileAllow bool
		wantErr   bool
	}{
		{"https ok", "https://github.com/x/y", false, false},
		{"file ok when allowed", "file:///tmp/x", true, false},
		{"file rejected when not allowed", "file:///tmp/x", false, true},
		{"https with userinfo rejected", "https://user@host/x", false, true},
		{"ftp rejected", "ftp://x/y", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allowFileScheme = c.fileAllow
			err := validateRepoURL(c.url)
			if c.wantErr && err == nil {
				t.Fatalf("validateRepoURL(%q) = nil, want error", c.url)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("validateRepoURL(%q) = %v, want nil", c.url, err)
			}
		})
	}
}

func TestRequireSafeTransport(t *testing.T) {
	cases := []struct {
		name              string
		scheme            string
		transportProvided bool
		installed         bool
		wantErr           bool
	}{
		{"https no transport no install → error", "https", false, false, true},
		{"https with transport → ok", "https", true, false, false},
		{"https with install → ok", "https", false, true, false},
		{"file no transport no install → ok", "file", false, false, false},
		{"file with transport → ok", "file", true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireSafeTransport(c.scheme, c.transportProvided, c.installed)
			if c.wantErr && err == nil {
				t.Fatalf("requireSafeTransport(%q, %v, %v) = nil, want error",
					c.scheme, c.transportProvided, c.installed)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("requireSafeTransport(%q, %v, %v) = %v, want nil",
					c.scheme, c.transportProvided, c.installed, err)
			}
		})
	}
}
