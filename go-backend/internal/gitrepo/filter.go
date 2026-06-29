package gitrepo

import (
	"bytes"
	"path"
	"strings"
)

const (
	GitRepoMaxFileBytes = 1 << 20 // 1 MiB
	GitRepoMaxFiles     = 2000
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, ".idea": true, ".vscode": true,
}

// extension -> mime type to record for the ingest pipeline.
var allowExt = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".rst": "text/plain",
	".txt": "text/plain", ".adoc": "text/plain",
	".go": "text/plain", ".py": "text/plain", ".js": "text/plain", ".ts": "text/plain",
	".tsx": "text/plain", ".jsx": "text/plain", ".java": "text/plain", ".c": "text/plain",
	".h": "text/plain", ".cpp": "text/plain", ".hpp": "text/plain", ".cs": "text/plain",
	".rb": "text/plain", ".rs": "text/plain", ".php": "text/plain", ".swift": "text/plain",
	".kt": "text/plain", ".scala": "text/plain", ".sh": "text/plain", ".sql": "text/plain",
	".yaml": "text/plain", ".yml": "text/plain", ".toml": "text/plain", ".ini": "text/plain",
	".cfg": "text/plain", ".json": "text/plain", ".xml": "text/plain", ".html": "text/html",
	".css": "text/plain", ".scss": "text/plain", ".vue": "text/plain", ".proto": "text/plain",
	".gradle": "text/plain", ".tf": "text/plain",
}

// allowNames are kept by exact base name even without an allowed extension.
var allowNames = map[string]string{
	"README": "text/markdown", "LICENSE": "text/plain",
	"Dockerfile": "text/plain", "Makefile": "text/plain", "CHANGELOG": "text/markdown",
}

// ShouldIngestPath decides whether a repo file path and size qualify for
// ingestion, returning (ok, mime). It checks the size cap, skip-dirs, extension
// allowlist, and known-name allowlist — but does NOT read the file content.
// Call this BEFORE reading the blob; only call IsBinaryContent on survivors.
func ShouldIngestPath(p string, size int) (bool, string) {
	if size > GitRepoMaxFileBytes {
		return false, ""
	}
	for _, seg := range strings.Split(p, "/") {
		if skipDirs[seg] {
			return false, ""
		}
	}
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(base))

	mime, ok := allowExt[ext]
	if !ok {
		// ext != "" means the extension is known-bad (e.g. ".png", ".gitignore" —
		// path.Ext returns the whole name for dotfiles). Only truly extensionless
		// names (README, Dockerfile) fall through to the allowNames lookup.
		if ext != "" {
			return false, ""
		}
		if mime, ok = allowNames[base]; !ok {
			return false, ""
		}
	}
	return true, mime
}

// IsBinaryContent returns true when content appears to be binary — i.e. when a
// NUL byte appears in the first ≤8 KiB of content.
func IsBinaryContent(content []byte) bool {
	sniff := content
	if len(sniff) > 8192 {
		sniff = sniff[:8192]
	}
	return bytes.IndexByte(sniff, 0) >= 0
}

// ShouldIngest decides whether a repo file is pulled into the KB and the mime
// type to record. content is the file's first bytes (≤8 KiB suffices) for the
// binary sniff; it may be the full content for small files.
// This is a thin wrapper around ShouldIngestPath + IsBinaryContent.
func ShouldIngest(p string, size int, content []byte) (bool, string) {
	ok, mime := ShouldIngestPath(p, size)
	if !ok {
		return false, ""
	}
	if IsBinaryContent(content) {
		return false, ""
	}
	return true, mime
}
