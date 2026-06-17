package chat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/splitter"
)

// errUnsupportedType is returned by parseAndSplitAttachment when no parser
// matches the uploaded file's MIME type / name. The TextParser catch-all means
// this is rare in practice, but kept so a future restricted factory can reject.
var errUnsupportedType = errors.New("chatattach: unsupported file type")

// errNoText is returned when parsing yields no usable text (empty file, or a
// scanned PDF that produced nothing extractable).
var errNoText = errors.New("chatattach: file contained no extractable text")

// parseAndSplitAttachment parses raw uploaded bytes into sections without KB
// ingestion. The parser API reads from a file path on disk (every Parser opens
// pctx.FilePath), so the bytes are spilled to a temp file that is removed before
// return. Returns the section slice (markdown-aware split when the parser flags
// the content as markdown) and the full trimmed text.
func parseAndSplitAttachment(ctx context.Context, factory *parser.Factory, filename, mimeType string, data []byte) (sections []string, full string, err error) {
	p := factory.GetParser(mimeType, filename)
	if p == nil {
		return nil, "", errUnsupportedType
	}

	// Spill to a temp file: the parsers (text, pdf, docx, …) all read from
	// pctx.FilePath rather than an in-memory reader. The suffix preserves the
	// original extension so extension-based parser routing inside Parse (where
	// any) behaves the same as the ingest path.
	ext := filepath.Ext(filename)
	// Guard: the extension is attacker-controlled (from the multipart filename).
	// Drop it if implausibly long or containing a path separator; parser routing
	// falls back to the MIME type inside Parse, so an empty suffix is safe.
	if len(ext) > 16 || strings.ContainsRune(ext, os.PathSeparator) || strings.ContainsRune(ext, '/') {
		ext = ""
	}
	tmp, err := os.CreateTemp("", "chatattach-*"+ext)
	if err != nil {
		return nil, "", err
	}
	tmpPath := tmp.Name()
	defer func() {
		if rmErr := os.Remove(tmpPath); rmErr != nil {
			logctx.From(ctx).Warn("chatattach: temp file cleanup failed", "path", tmpPath, "error", rmErr)
		}
	}()
	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		return nil, "", werr
	}
	if cerr := tmp.Close(); cerr != nil {
		return nil, "", cerr
	}

	res, err := p.Parse(ctx, parser.ParseContext{
		FilePath: tmpPath,
		FileName: filename,
		MimeType: mimeType,
		FileSize: int64(len(data)),
	})
	if err != nil {
		return nil, "", err
	}

	text := strings.TrimSpace(res.Text)
	if text == "" {
		return nil, "", errNoText
	}

	cfg := splitter.DefaultConfig()
	if res.IsMarkdown {
		cfg = splitter.MarkdownConfig()
	}
	return splitter.Split(text, cfg), text, nil
}

// uploadAttachmentResponse is the wire shape returned on a successful upload.
type uploadAttachmentResponse struct {
	AttachmentID string `json:"attachmentId"`
	Filename     string `json:"filename"`
	SectionCount int    `json:"sectionCount"`
	CharCount    int    `json:"charCount"`
}

// UploadAttachment handles POST /api/kb/{id}/chat/attachment. It accepts a
// single multipart "file" field, parses + splits it in memory (no KB
// ingestion), stores it in the chatattach store, and returns the attachment id
// plus metadata for the comparison flow.
//
// Auth + KB-view permission are enforced by route middleware (mirroring
// SendMessage, whose comment notes the same), so this handler only re-checks
// that an authenticated user is present.
func (h *Handler) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !CompareEnabled(ctx, h.siteConfigReader) {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "document comparison is disabled")
		return
	}

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	kbID := r.PathValue("id")
	if kbID == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "missing knowledge base id")
		return
	}

	ctx = logctx.WithKB(ctx, kbID)
	ctx = logctx.WithUser(ctx, user.ID)

	maxBytes := int64(CompareMaxFileBytes(ctx, h.siteConfigReader))
	// Cap the request body before any read so a too-large upload is rejected
	// rather than buffered whole. ParseMultipartForm reads through this reader.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httputil.WriteErrorCtx(ctx, w, http.StatusRequestEntityTooLarge, "file is too large")
			return
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, hdr, err := r.FormFile("file")
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			httputil.WriteErrorCtx(ctx, w, http.StatusRequestEntityTooLarge, "file is too large")
			return
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}

	mimeType := hdr.Header.Get("Content-Type")
	sections, full, err := parseAndSplitAttachment(ctx, h.attachmentFactory(), hdr.Filename, mimeType, data)
	switch {
	case errors.Is(err, errUnsupportedType):
		httputil.WriteErrorCtx(ctx, w, http.StatusUnsupportedMediaType, "unsupported file type")
		return
	case errors.Is(err, errNoText):
		httputil.WriteErrorCtx(ctx, w, http.StatusUnprocessableEntity, "the file contained no extractable text")
		return
	case err != nil:
		logctx.From(ctx).Error("chatattach: parse failed", "filename", hdr.Filename, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to parse file")
		return
	}

	if h.attachmentStore == nil {
		logctx.From(ctx).Error("chatattach: attachmentStore not wired")
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "feature not fully initialized")
		return
	}

	id, err := h.attachmentStore.Put(ctx, chatattach.Attachment{
		UserID:   user.ID,
		KbID:     kbID,
		Filename: hdr.Filename,
		MimeType: mimeType,
		FullText: full,
		Sections: sections,
	})
	if err != nil {
		logctx.From(ctx).Error("chatattach: store put failed", "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to store attachment")
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, uploadAttachmentResponse{
		AttachmentID: id,
		Filename:     hdr.Filename,
		SectionCount: len(sections),
		CharCount:    len(full),
	})
}

// attachmentFactory returns the handler's parser factory, lazily falling back
// to the default built-in factory when one was not injected. The default
// factory needs no heavy dependencies (nil transcriber: audio uploads are not
// expected in the comparison flow), so this keeps UploadAttachment working
// before the constructor wiring (a later task) sets the field explicitly.
func (h *Handler) attachmentFactory() *parser.Factory {
	if h.parserFactory != nil {
		return h.parserFactory
	}
	return parser.DefaultFactoryWith(nil)
}
