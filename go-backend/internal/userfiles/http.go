package userfiles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/uploadcheck"
)

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// Handler serves the /api/library HTTP API. Every route is owner-only: ids
// resolve through Store.Get(ownerID, id), so another owner's id and a
// malformed id are both a 404.
type Handler struct {
	store    Store
	ingester *Ingester
	stor     storage.Storage
	limits   uploadcheck.Limits
	quota    QuotaReader
	deleter  FileDeleter
}

func NewHandler(store Store, in *Ingester, stor storage.Storage, limits uploadcheck.Limits, quota QuotaReader) *Handler {
	return &Handler{store: store, ingester: in, stor: stor, limits: limits, quota: quota}
}

// FileDeleter removes a library file together with every KB copy of it
// (implemented by *cascade.Deleter).
type FileDeleter interface {
	DeleteUserFile(ctx context.Context, ownerID, userFileID string) error
}

// SetDeleter wires the cascade deleter behind DELETE /api/library/files/{id}.
func (h *Handler) SetDeleter(d FileDeleter) { h.deleter = d }

// Delete handles DELETE /api/library/files/{id}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id) || h.deleter == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File not found")
		return
	}
	err := h.deleter.DeleteUserFile(r.Context(), uid, id)
	if errors.Is(err, cascade.ErrUserFileNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File not found")
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// userID returns the authenticated user's id or writes a 401.
func userID(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return "", false
	}
	return u.ID, true
}

// WriteQuotaExceeded writes the 413 quota body shared by every endpoint that
// can grow a user's library (library upload, KB upload).
func WriteQuotaExceeded(ctx context.Context, w http.ResponseWriter, qe *QuotaError) {
	httputil.WriteJSONCtx(ctx, w, http.StatusRequestEntityTooLarge, map[string]any{
		"error":      "quota_exceeded",
		"usedBytes":  qe.UsedBytes,
		"quotaBytes": qe.QuotaBytes,
	})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File not found")
		return
	}
	httputil.WriteInternalErrorCtx(r.Context(), w, err)
}

// List handles GET /api/library/files.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	limit := defaultListLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = min(max(n, 1), maxListLimit)
	}
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = n
	}
	items, total, err := h.store.List(r.Context(), uid, limit, offset)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []UserFile{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// uploadResponse is a UserFile plus the optional dedup marker.
type uploadResponse struct {
	*UserFile
	Deduplicated bool `json:"deduplicated,omitempty"`
}

// Upload handles POST /api/library/files.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	up, err := uploadcheck.Parse(w, r, h.limits)
	if err != nil {
		var ue *uploadcheck.Error
		if errors.As(err, &ue) {
			httputil.WriteErrorCtx(r.Context(), w, ue.Status, ue.Message)
			return
		}
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	defer up.File.Close()

	row, created, err := h.ingester.Ingest(r.Context(), uid, up)
	if err != nil {
		var qe *QuotaError
		if errors.As(err, &qe) {
			WriteQuotaExceeded(r.Context(), w, qe)
			return
		}
		h.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	httputil.WriteJSONCtx(r.Context(), w, status, uploadResponse{UserFile: row, Deduplicated: !created})
}

// Get handles GET /api/library/files/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	row, err := h.store.Get(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, row)
}

// Rename handles PATCH /api/library/files/{id}.
func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid name")
		return
	}
	row, err := h.store.Rename(r.Context(), uid, r.PathValue("id"), body.Name)
	if errors.Is(err, ErrInvalidName) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid name")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, row)
}

// sanitizeFilename mirrors files.sanitizeContentDispositionFilename: a stray
// double quote would break out of the quoted token and control characters
// confuse some clients.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
}

// rfc5987Escape percent-encodes name for a filename* parameter. PathEscape
// leaves ' ( ) * and a few more unescaped, which are not RFC 5987 attr-chars.
func rfc5987Escape(name string) string {
	r := strings.NewReplacer("'", "%27", "(", "%28", ")", "%29", "*", "%2A", "!", "%21", "$", "%24", "&", "%26", "+", "%2B", "=", "%3D", ":", "%3A", "@", "%40")
	return r.Replace(url.PathEscape(name))
}

// Download handles GET /api/library/files/{id}/download.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	row, err := h.store.Get(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if row.StoragePath == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "File has no storage path")
		return
	}
	stream, err := h.stor.ReadFileStream(r.Context(), row.StoragePath)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	defer stream.Close()

	contentType := row.Mime
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// ASCII fallback (same sanitising as the KB download) plus the RFC 5987
	// form so non-ASCII names survive.
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(row.Name)+
		`"; filename*=UTF-8''`+rfc5987Escape(row.Name))
	w.Header().Set("Content-Type", contentType)
	// Restrictive CSP on user-uploaded files to prevent XSS if opened in browser.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; object-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, stream); err != nil {
		logctx.From(r.Context()).Warn("library download: copy failed", "error", err)
	}
}

// Usage handles GET /api/library/files/{id}/usage.
func (h *Handler) Usage(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	kbs, err := h.store.Usage(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if kbs == nil {
		kbs = []KBUsage{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"kbs": kbs})
}

// Quota handles GET /api/library/quota (quotaBytes 0 = unlimited).
func (h *Handler) Quota(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(w, r)
	if !ok {
		return
	}
	used, err := h.store.UsedBytes(r.Context(), uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q, err := EffectiveQuota(r.Context(), h.store, h.quota, uid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"usedBytes": used, "quotaBytes": q})
}
