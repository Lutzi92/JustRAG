package siteconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

var publicConfigKeys = map[string]bool{
	"logo_path":               true,
	"imprint":                 true,
	"kb_header":               true,
	"example_prompts":         true,
	"chat_footer":             true,
	"confluence_enabled":      true,
	"web_search_enabled":      true,
	"academic_search_enabled": true,
	"academic_search_name":    true,
}

// sensitiveConfigKeys hold secrets that must never be echoed back to a client,
// not even to an admin. GetSiteConfig returns the mask placeholder for any
// non-empty value; UpdateSiteConfig treats an incoming mask as "unchanged" and
// preserves the stored value.
var sensitiveConfigKeys = map[string]bool{
	"google_search_api_key": true,
}

// siteConfigSecretMask is the placeholder substituted for sensitive values in
// API responses. It is also recognised on write as "keep the existing value".
const siteConfigSecretMask = "********"

// KeyValue is a key/value pair used for batch config updates.
// Value is nil when the client sends null (to clear a config key).
type KeyValue struct {
	Key   string
	Value *string
}

// SiteConfigRow is a single row returned from the site_configs table.
type SiteConfigRow struct {
	Key   string
	Value *string
}

// Store is the persistence interface used by the siteconfig handlers.
type Store interface {
	GetAllSiteConfigs(ctx context.Context) ([]SiteConfigRow, error)
	UpdateSiteConfigsBatch(ctx context.Context, configs []KeyValue) ([]SiteConfigRow, error)
	UpdateSiteConfig(ctx context.Context, key, value string) error
}

type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) GetSiteConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rows, err := h.store.GetAllSiteConfigs(ctx)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to fetch site config")
		return
	}

	user := auth.UserFromContext(ctx)
	isAdmin := user != nil && (user.Role == "admin" || user.Role == "superadmin")

	result := make(map[string]string)
	for _, row := range rows {
		if row.Value == nil {
			continue
		}
		if !isAdmin && !publicConfigKeys[row.Key] {
			continue
		}
		// Never send a secret to the client — admins included. The admin UI
		// renders the mask and only writes a real value back when changed.
		if sensitiveConfigKeys[row.Key] && *row.Value != "" {
			result[row.Key] = siteConfigSecretMask
			continue
		}
		result[row.Key] = *row.Value
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}

// UpdateSiteConfig handles POST /api/site-config.
// Body: {"configs": {"key1": "value1", ...}}
// Requires superadmin (enforced by middleware in main.go).
func (h *Handler) UpdateSiteConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		Configs map[string]any `json:"configs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if body.Configs == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "configs is required")
		return
	}

	kvs := make([]KeyValue, 0, len(body.Configs))
	for k, v := range body.Configs {
		if v == nil {
			kvs = append(kvs, KeyValue{Key: k, Value: nil})
			continue
		}
		switch val := v.(type) {
		case string:
			// A sensitive key submitted as the mask means "unchanged" — skip it
			// so we don't overwrite the stored secret with the placeholder.
			if sensitiveConfigKeys[k] && val == siteConfigSecretMask {
				continue
			}
			kvs = append(kvs, KeyValue{Key: k, Value: &val})
		case float64: // JSON numbers decode as float64
			s := fmt.Sprintf("%v", val)
			kvs = append(kvs, KeyValue{Key: k, Value: &s})
		case bool:
			s := fmt.Sprintf("%v", val)
			kvs = append(kvs, KeyValue{Key: k, Value: &s})
		default:
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("config value for key %q must be a string, number, boolean, or null", k))
			return
		}
	}

	// Reject batches that would enable both halves of a documented
	// mutually-exclusive flag pair (raptor vs parent-child, Self-RAG vs
	// factuality verifier). Validation needs the merged view, so fetch the
	// current table first; on fetch failure fall through to the save rather
	// than blocking config writes on a read error — the runtime skip logic
	// still guards against the incoherent combinations.
	if existing, err := h.store.GetAllSiteConfigs(ctx); err == nil {
		existingMap := make(map[string]*string, len(existing))
		for _, row := range existing {
			existingMap[row.Key] = row.Value
		}
		if err := ValidateConflicts(existingMap, kvs); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
			return
		}
	}

	rows, err := h.store.UpdateSiteConfigsBatch(ctx, kvs)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to update site config")
		return
	}

	result := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Value != nil {
			result[row.Key] = *row.Value
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}

// sanitizeFilename replaces path separators with underscores and then strips
// any character that is not alphanumeric, space, dot, dash, or underscore.
var reUnsafe = regexp.MustCompile(`[^a-zA-Z0-9 .\-_]`)

func sanitizeFilename(name string) string {
	name = strings.NewReplacer("/", "_", `\`, "_").Replace(name)
	return reUnsafe.ReplaceAllString(name, "_")
}

// UploadLogo handles POST /api/site-config/logo.
// Request: multipart/form-data with a "logo" file field (.svg or .png, max 2 MB).
// Requires superadmin (enforced by middleware in main.go).
func (h *Handler) UploadLogo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	const maxLogoSize = 2 << 20 // 2 MB

	// Reject oversized requests early via Content-Length.
	if r.ContentLength > maxLogoSize+4096 { // small buffer for multipart overhead
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File too large")
		return
	}

	// Wrap the body with a hard size limit so even chunked uploads can't exceed 2 MB.
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxLogoSize)+4096)

	if err := r.ParseMultipartForm(maxLogoSize); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File too large or invalid multipart form")
		return
	}

	file, header, err := r.FormFile("logo")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "logo file is required")
		return
	}
	defer file.Close()

	if header.Size > int64(maxLogoSize) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "File too large")
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".svg" && ext != ".png" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Only .svg and .png files are allowed")
		return
	}

	// Build destination path.
	dir := "uploads/logo"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to create upload directory")
		return
	}

	ts := time.Now().UnixMilli()
	filename := sanitizeFilename(fmt.Sprintf("logo_%d%s", ts, ext))
	destPath := filepath.Join(dir, filename)

	dst, err := os.Create(destPath)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to save file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to write file")
		return
	}

	logoPath := "/uploads/logo/" + filename
	if err := h.store.UpdateSiteConfig(ctx, "logo_path", logoPath); err != nil {
		// Clean up the orphaned file since the DB update failed
		os.Remove(destPath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to update logo_path config")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]string{
		"success":  "true",
		"logoPath": logoPath,
	})
}
