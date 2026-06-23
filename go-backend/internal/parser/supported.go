package parser

import (
	"path/filepath"
	"strings"
)

// SupportedExtensions is the canonical allowlist of file extensions JustRAG can
// ingest into a knowledge base. It mirrors the parsers registered in
// DefaultFactoryWith plus the plain-text extensions handled by TextParser.
//
// This list is the single source of truth for upload-time validation: the
// factory's TextParser is a catch-all that accepts *anything*, so it cannot be
// used to decide whether a file is "supported" — an unsupported binary like a
// legacy .doc would silently fall through to it and be ingested as garbage. The
// upload handler consults this allowlist instead (see IsSupportedExtension) to
// reject such files up front rather than letting them sit in a KB with an
// ingestion error.
//
// Extensions are lowercase and include the leading dot. Dangerous executable /
// script extensions (.exe, .sh, .html, …) are intentionally absent — they are
// rejected separately by the upload handler's dangerous-extension blocklist,
// which runs first.
var SupportedExtensions = map[string]bool{
	// Documents
	".pdf":  true,
	".docx": true,
	".pptx": true,
	".odt":  true,
	".epub": true,
	".tex":  true,
	// Spreadsheets / tabular
	".xlsx": true,
	".xls":  true,
	".ods":  true,
	".csv":  true,
	// Images
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".bmp":  true,
	".tif":  true,
	".tiff": true,
	".pnm":  true,
	".gif":  true,
	// Audio (transcribed)
	".mp3":  true,
	".wav":  true,
	".m4a":  true,
	".mp4":  true,
	".mpeg": true,
	".mpga": true,
	".ogg":  true,
	".oga":  true,
	".flac": true,
	".webm": true,
	// Plain text / markup / config (TextParser)
	".txt":      true,
	".md":       true,
	".markdown": true,
	".mdx":      true,
	".json":     true,
	".log":      true,
	".yaml":     true,
	".yml":      true,
	".ini":      true,
	".cfg":      true,
	".conf":     true,
	".toml":     true,
	".env":      true,
	".zsh":      true,
	".fish":     true,
}

// IsSupportedExtension reports whether fileName has an extension JustRAG can
// ingest. A file with no extension is allowed: such files (README, Dockerfile,
// LICENSE, …) are commonly plain text and handled by TextParser's fallback.
// Any other unrecognized extension (e.g. ".doc") returns false.
func IsSupportedExtension(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		return true
	}
	return SupportedExtensions[ext]
}
