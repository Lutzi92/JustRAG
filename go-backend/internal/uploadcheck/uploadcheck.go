// Package uploadcheck validates a multipart file upload. It is shared by the
// KB upload endpoint (internal/files) and the user file library upload
// endpoint (internal/userfiles), which cannot import each other.
package uploadcheck

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/parser"
)

// MaxUploadSize is the hard transport cap http.MaxBytesReader enforces
// against every upload, regardless of file type. A var (not a const) so a
// test can shrink it without a 500 MB request body. Production code never
// mutates it.
var MaxUploadSize int64 = 500 << 20 // 500 MB

// MaxFileNameBytes bounds the user-supplied file name. Matches the
// files.name varchar(255) column so over-long values are rejected with a 400
// rather than failing as a DB constraint violation (500).
const MaxFileNameBytes = 255

// Limits supplies the per-type size gates read from site_config. Narrow by
// design: this package must not import internal/chat (the SiteConfigReader's
// home package) merely to read one int.
type Limits interface {
	// TabularMaxFileBytes returns the configured maximum size, in bytes, for
	// an uploaded spreadsheet.
	TabularMaxFileBytes(ctx context.Context) int
}

// Upload is a validated multipart file.
type Upload struct {
	File     multipart.File
	Header   *multipart.FileHeader
	MimeType string // extension-derived, "application/octet-stream" fallback
}

// Error is a validation failure carrying the exact HTTP status + message the
// KB upload endpoint has always returned.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(status int, msg string) (*Upload, error) {
	return nil, &Error{Status: status, Message: msg}
}

// dangerousExtensions is the set of file extensions that are rejected on upload.
// Matches the Node.js blocklist to prevent XSS and code execution via uploaded files.
var dangerousExtensions = map[string]bool{
	// Executables & scripts
	".exe": true, ".bat": true, ".cmd": true, ".com": true,
	".sh": true, ".bash": true, ".ps1": true, ".psm1": true, ".psd1": true,
	".vbs": true, ".msi": true, ".dll": true, ".scr": true, ".hta": true,
	".wsf": true, ".wsh": true, ".jar": true,
	// Web / scripting languages
	".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".html": true, ".htm": true, ".xhtml": true, ".phtml": true,
	".xml": true, ".svg": true,
	".php": true, ".pl": true, ".py": true, ".rb": true,
	".asp": true, ".aspx": true, ".jsp": true, ".jspx": true,
}

// ValidateName applies the filename rules every stored file must satisfy:
// no dangerous extension, a supported extension, and at most MaxFileNameBytes
// bytes. It returns an *Error carrying the exact upload message, or nil.
func ValidateName(name string) *Error {
	ext := strings.ToLower(filepath.Ext(name))
	if dangerousExtensions[ext] {
		return &Error{Status: http.StatusBadRequest, Message: "File type not allowed"}
	}
	// Without the supported check an unsupported binary (e.g. a legacy .doc)
	// would be queued for ingestion and fail or be indexed as garbage.
	if !parser.IsSupportedExtension(name) {
		return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf("File type not supported (%s)", ext)}
	}
	// files.name is varchar(255); reject cleanly instead of a DB 500.
	if len(name) > MaxFileNameBytes {
		return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf("Filename must not exceed %d bytes", MaxFileNameBytes)}
	}
	return nil
}

// humanBytes renders n as a human-readable KB/MB/GB size with one decimal
// place, trimming a trailing ".0" (500.0 MB -> "500 MB") so exact values
// read cleanly. Values under 1 KB render as whole bytes.
func humanBytes(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
		gb = 1 << 30
	)
	var unit string
	var div float64
	switch {
	case n >= gb:
		unit, div = "GB", gb
	case n >= mb:
		unit, div = "MB", mb
	case n >= kb:
		unit, div = "KB", kb
	default:
		return fmt.Sprintf("%d B", n)
	}
	s := strconv.FormatFloat(float64(n)/div, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + unit
}

// Parse applies http.MaxBytesReader(MaxUploadSize), parses the multipart
// form, reads field "file", and runs every content check (empty, dangerous
// extension, supported extension, filename length, tabular size gate).
// limits may be nil (no tabular gate). On failure it returns *Error; the
// caller writes it verbatim. The caller must Close() Upload.File.
func Parse(w http.ResponseWriter, r *http.Request, limits Limits) (*Upload, error) {
	// 1. Enforce hard request body limit, then parse multipart form.
	// MaxBytesReader rejects bodies exceeding the cap (ParseMultipartForm's
	// argument only controls the in-memory buffering threshold, not the total size).
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB in-memory threshold
		logctx.From(r.Context()).Warn("upload: parse multipart form failed", "error", err)
		// Ruling R70: the transport-wide body cap is a 413 for EVERY upload,
		// not a 400 — the previous "File too large or invalid multipart
		// form" message conflated the two and always answered 400, even
		// when the body was rejected purely for size by MaxBytesReader.
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return fail(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("File too large: the upload limit is %s", humanBytes(mbe.Limit)))
		}
		return fail(http.StatusBadRequest, "Invalid multipart form")
	}

	// 2. Get the file from the form.
	file, header, err := r.FormFile("file")
	if err != nil {
		return fail(http.StatusBadRequest, "File field is required")
	}
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()

	// 3a. Reject empty files.
	if header.Size == 0 {
		return fail(http.StatusBadRequest, "File must not be empty")
	}

	// 3b-3c. Reject dangerous / unsupported extensions and over-long names.
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if verr := ValidateName(header.Filename); verr != nil {
		return nil, verr
	}

	// Detect MIME type from extension (fall back to octet-stream). Computed
	// here — before the spreadsheet size gate below — and reused verbatim at
	// the storage step, so the gate and the processor's later
	// CanParse(mimeType, fileName) call (internal/processor/processor.go)
	// agree on the same predicate. On a host whose MIME database maps a
	// legacy extension like .xlt/.xlm/.xla/.xlc/.xlw to
	// "application/vnd.ms-excel" (in parser.spreadsheetMIMEs), CanParse
	// matches only via the MIME argument — the extension switch does not
	// include those.
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// 3d. Reject oversize spreadsheets against the tabular_max_file_bytes
	// knob. Spreadsheet-specific: the materializer, not the generic
	// chunk/embed ingest path, is what an oversize spreadsheet would blow up
	// (memory-buffered parsing), so this does NOT apply to other file
	// types — those stay governed only by MaxUploadSize / the KB size cap.
	if limits != nil && (&parser.SpreadsheetParser{}).CanParse(mimeType, header.Filename) {
		if limit := limits.TabularMaxFileBytes(r.Context()); limit > 0 && header.Size > int64(limit) {
			return fail(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Spreadsheet too large (%s): the limit is %s (tabular_max_file_bytes)",
					humanBytes(header.Size), humanBytes(int64(limit))))
		}
	}

	ok = true
	return &Upload{File: file, Header: header, MimeType: mimeType}, nil
}
