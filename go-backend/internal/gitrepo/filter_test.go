package gitrepo

import "testing"

func TestShouldIngest(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		size    int
		content []byte
		wantOK  bool
	}{
		{"markdown", "docs/readme.md", 100, []byte("# hi"), true},
		{"go source", "internal/app/main.go", 100, []byte("package main"), true},
		{"readme no ext", "README", 100, []byte("hello"), true},
		{"dockerfile", "Dockerfile", 100, []byte("FROM x"), true},
		{"node_modules skipped", "node_modules/lib/index.js", 100, []byte("x"), false},
		{"git dir skipped", ".git/config", 100, []byte("x"), false},
		{"vendor skipped", "vendor/foo/bar.go", 100, []byte("x"), false},
		{"dist skipped", "dist/app.js", 100, []byte("x"), false},
		{"png binary ext", "img/logo.png", 100, []byte("\x89PNG"), false},
		{"unknown ext", "data.bin", 100, []byte("abc"), false},
		{"oversize", "big.md", GitRepoMaxFileBytes + 1, []byte("# big"), false},
		{"nul byte binary", "weird.txt", 100, []byte("ab\x00cd"), false},
		{"dotfile config skipped", ".gitignore", 50, []byte("node_modules"), false},
		{"yaml config", "ci/build.yaml", 100, []byte("steps: []"), true},
		{"not-substring skip dir", "src/node_modules_helper/x.go", 100, []byte("package x"), true},
		{"not-substring vendor", "src/vendor_copy/main.go", 100, []byte("package main"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, _ := ShouldIngest(c.path, c.size, c.content)
			if ok != c.wantOK {
				t.Fatalf("ShouldIngest(%q) = %v, want %v", c.path, ok, c.wantOK)
			}
		})
	}
}

func TestShouldIngestMimeType(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		size     int
		content  []byte
		wantOK   bool
		wantMime string
	}{
		{"markdown extension", "a.md", 10, []byte("# x"), true, "text/markdown"},
		{"html extension", "a.html", 10, []byte("<p>"), true, "text/html"},
		{"go source", "a.go", 10, []byte("package x"), true, "text/plain"},
		{"readme no ext", "README", 10, []byte("hello"), true, "text/markdown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, mime := ShouldIngest(c.path, c.size, c.content)
			if ok != c.wantOK {
				t.Errorf("ShouldIngest(%q) ok = %v, want %v", c.path, ok, c.wantOK)
			}
			if mime != c.wantMime {
				t.Errorf("ShouldIngest(%q) mime = %q, want %q", c.path, mime, c.wantMime)
			}
		})
	}
}
