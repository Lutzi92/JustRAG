// Package gencontent provides HTTP handlers for managing generated content records
// (presentations, podcasts, etc.) stored in the generated_content table.
package gencontent

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// GenContentRow represents a single row from the generated_content table.
type GenContentRow struct {
	ID        string    `json:"id" db:"id"`
	KbID      string    `json:"kbId" db:"kb_id"`
	UserID    string    `json:"userId" db:"user_id"`
	Type      string    `json:"type" db:"type"`
	Title     string    `json:"title" db:"title"`
	Content   any       `json:"content" db:"content"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// Store is the persistence interface used by the generated content handlers.
type Store interface {
	GetGenContentByID(ctx context.Context, id string) (*GenContentRow, error)
	UpdateGenContent(ctx context.Context, id string, content any) (*GenContentRow, error)
	DeleteGenContent(ctx context.Context, id string) error
	ListGeneratedContent(ctx context.Context, kbID, userID string) ([]GeneratedContentItem, error)
}

// Handler holds the dependencies for the generated content endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a new Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// allowedStorageRoots are the directory prefixes that generated content files
// may reside in. Any path outside these roots is rejected to prevent path
// traversal attacks via PATCH /api/generated-content/{id}.
var allowedStorageRoots = []string{"data/", "uploads/", "/app/data/", "/app/uploads/"}

// isAllowedPath checks that a file path starts with one of the allowed roots
// and does not contain path traversal sequences after cleaning.
func isAllowedPath(p string) bool {
	cleaned := filepath.Clean(p)
	if strings.Contains(cleaned, "..") {
		return false
	}
	for _, root := range allowedStorageRoots {
		if strings.HasPrefix(cleaned, root) || strings.HasPrefix(cleaned, filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// extractFilePath inspects the content JSONB map and returns the first file path
// found under the keys "filePath" or "audioPath". Returns ("", false, false) when
// the content is not a map, neither key is present, or the path is outside
// allowed storage roots (prevents path traversal via user-controlled content updates).
func extractFilePath(content any) (path string, isAudio bool, ok bool) {
	m, ok := content.(map[string]any)
	if !ok {
		return "", false, false
	}
	if v, exists := m["filePath"]; exists {
		if s, isStr := v.(string); isStr && s != "" && isAllowedPath(s) {
			return s, false, true
		}
	}
	if v, exists := m["audioPath"]; exists {
		if s, isStr := v.(string); isStr && s != "" && isAllowedPath(s) {
			return s, true, true
		}
	}
	return "", false, false
}

// ListGenContent handles GET /api/kb/{id}/generated-content. The KB id is
// resolved from kbaccess (preferred — middleware has already authorized the
// caller for that KB) or falls back to the URL PathValue. Items are filtered
// to the calling user.
func (h *Handler) ListGenContent(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}
	rows, err := h.store.ListGeneratedContent(r.Context(), kbID, user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rows)
}

// UpdateGenContent handles PATCH /api/generated-content/{id}.
// Body: {"content": {...}}
// Verifies ownership (content.userId == caller.id), then replaces the content JSONB field.
func (h *Handler) UpdateGenContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	existing, err := h.store.GetGenContentByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch generated content")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "generated content not found")
		return
	}

	if existing.UserID != caller.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
		return
	}

	var body struct {
		Content any `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Content == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "content is required")
		return
	}

	updated, err := h.store.UpdateGenContent(ctx, id, body.Content)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update generated content")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, updated)
}

// DeleteGenContent handles DELETE /api/generated-content/{id}.
// Verifies ownership, then deletes the record (cascade handled by DB FK).
func (h *Handler) DeleteGenContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	existing, err := h.store.GetGenContentByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch generated content")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "generated content not found")
		return
	}

	if existing.UserID != caller.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
		return
	}

	if err := h.store.DeleteGenContent(ctx, id); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete generated content")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DownloadGenContent handles GET /api/generated-content/{id}/download.
// Verifies ownership, extracts filePath or audioPath from the content JSONB,
// and streams the file to the client as an attachment download.
func (h *Handler) DownloadGenContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	existing, err := h.store.GetGenContentByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch generated content")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "generated content not found")
		return
	}

	if existing.UserID != caller.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
		return
	}

	filePath, _, ok := extractFilePath(existing.Content)
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "no file available for download")
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "file not found on disk")
			return
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to stat file")
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(filePath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	filename := filepath.Base(filePath)
	// Use mime.FormatMediaType so unicode filenames are encoded correctly
	// (RFC 2231) instead of producing an invalid Content-Disposition header.
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	// http.ServeContent sets Content-Length, handles Range requests, and
	// negotiates conditional GETs from Last-Modified — much better behaviour
	// than a bare io.Copy for clients that pre-fetch metadata.
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

// StreamGenContent handles GET /api/generated-content/{id}/stream.
// Verifies ownership, extracts the audio path from the content JSONB,
// and streams the audio file inline (Content-Disposition: inline).
func (h *Handler) StreamGenContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	existing, err := h.store.GetGenContentByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch generated content")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "generated content not found")
		return
	}

	if existing.UserID != caller.ID {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
		return
	}

	filePath, _, ok := extractFilePath(existing.Content)
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "no audio file available for streaming")
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "file not found on disk")
			return
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to stat file")
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(filePath))
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	// HTML5 <audio> elements need Range request support and a known
	// Content-Length to display controls and start playback. http.ServeContent
	// provides both; the previous bare io.Copy left the browser unable to
	// render the player for many users.
	http.ServeContent(w, r, filepath.Base(filePath), stat.ModTime(), f)
}
