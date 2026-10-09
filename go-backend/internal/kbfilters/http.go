package kbfilters

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// maxCategoryNameLen mirrors kb_user_categories.name varchar(100). Checked
// here so an over-long chip label is a 400 rather than a 500 from SQLSTATE
// 22001.
const maxCategoryNameLen = 100

// Handler serves the per-user filter endpoints.
//
// Two access shapes:
//
//   - The /api/kb-user-categories routes carry no KB in the path at all. They
//     touch only rows the caller owns, so the authenticate middleware is the
//     whole gate (same pattern as GET /api/kb and GET /api/kb/catalog).
//   - The /api/kb/{id}/... routes sit on kbViewChain. Starring or tagging is
//     not a privilege, but it should only be possible on a topic the caller
//     can open, and kbaccess.RequireKBRole(view) is the rule that already
//     decides that: 404 for a KB that does not exist, 403 for one the caller
//     may not see. Those are the same answers every other {id} route gives,
//     so these routes reveal nothing the rest of the API does not.
type Handler struct {
	store Store
}

// NewHandler creates a Handler over store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// categoryRequest is the create/update body. SortOrder is decoded as int64 so
// an out-of-range value reaches the explicit int32 check below as a 400,
// rather than as a decode error or SQLSTATE 22003 on the int column.
type categoryRequest struct {
	Name      string `json:"name"`
	SortOrder int64  `json:"sortOrder"`
}

// ---------------------------------------------------------------------------
// Favorites
// ---------------------------------------------------------------------------

// AddFavorite handles PUT /api/kb/{id}/favorite.
func (h *Handler) AddFavorite(w http.ResponseWriter, r *http.Request) {
	h.setFavorite(w, r, true)
}

// RemoveFavorite handles DELETE /api/kb/{id}/favorite.
func (h *Handler) RemoveFavorite(w http.ResponseWriter, r *http.Request) {
	h.setFavorite(w, r, false)
}

func (h *Handler) setFavorite(w http.ResponseWriter, r *http.Request, on bool) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	// The KB id is taken from the access result rather than the path value:
	// that is the row kbViewChain actually resolved and authorised, so the two
	// can never drift apart.
	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.KB == nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("kbfilters: missing KB access result"))
		return
	}

	var err error
	if on {
		err = h.store.AddFavorite(ctx, user.ID, access.KB.ID)
	} else {
		err = h.store.RemoveFavorite(ctx, user.ID, access.KB.ID)
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to update favorite: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// User categories
// ---------------------------------------------------------------------------

// ListCategories handles GET /api/kb-user-categories.
func (h *Handler) ListCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	cats, err := h.store.ListCategories(ctx, user.ID)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to list categories: %w", err))
		return
	}
	// [] rather than null: the chip row maps over the response directly.
	if cats == nil {
		cats = []UserCategory{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, cats)
}

// CreateCategory handles POST /api/kb-user-categories.
func (h *Handler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	body, ok := decodeCategoryRequest(w, r)
	if !ok {
		return
	}

	cat, err := h.store.CreateCategory(ctx, user.ID, body.Name, int32(body.SortOrder))
	switch {
	case errors.Is(err, ErrDuplicateName):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "a category with that name already exists")
	case errors.Is(err, ErrCategoryLimit):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict,
			fmt.Sprintf("category limit reached (%d per user)", MaxCategoriesPerUser))
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to create category: %w", err))
	default:
		httputil.WriteJSONCtx(ctx, w, http.StatusCreated, cat)
	}
}

// UpdateCategory handles PATCH /api/kb-user-categories/{catId}. Name and
// sortOrder are both replaced; there is no field-level patching, because the
// only editor is a two-field rename dialog.
func (h *Handler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	catID, ok := pathUUID(w, r, "catId")
	if !ok {
		return
	}
	body, ok := decodeCategoryRequest(w, r)
	if !ok {
		return
	}

	cat, err := h.store.UpdateCategory(ctx, user.ID, catID, body.Name, int32(body.SortOrder))
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "category not found")
	case errors.Is(err, ErrDuplicateName):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "a category with that name already exists")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to update category: %w", err))
	default:
		httputil.WriteJSONCtx(ctx, w, http.StatusOK, cat)
	}
}

// DeleteCategory handles DELETE /api/kb-user-categories/{catId}.
func (h *Handler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	catID, ok := pathUUID(w, r, "catId")
	if !ok {
		return
	}

	err := h.store.DeleteCategory(ctx, user.ID, catID)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "category not found")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to delete category: %w", err))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// Assignments
// ---------------------------------------------------------------------------

// AssignCategory handles PUT /api/kb/{id}/user-categories/{catId}.
func (h *Handler) AssignCategory(w http.ResponseWriter, r *http.Request) {
	h.setAssignment(w, r, true)
}

// UnassignCategory handles DELETE /api/kb/{id}/user-categories/{catId}.
func (h *Handler) UnassignCategory(w http.ResponseWriter, r *http.Request) {
	h.setAssignment(w, r, false)
}

func (h *Handler) setAssignment(w http.ResponseWriter, r *http.Request, on bool) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.KB == nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("kbfilters: missing KB access result"))
		return
	}
	catID, ok := pathUUID(w, r, "catId")
	if !ok {
		return
	}

	var err error
	if on {
		err = h.store.AssignCategory(ctx, user.ID, catID, access.KB.ID)
	} else {
		err = h.store.UnassignCategory(ctx, user.ID, catID, access.KB.ID)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		// Raised by the composite FK when the category is somebody else's.
		// Same 404 as a genuinely missing id, on purpose.
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "category not found")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to update category assignment: %w", err))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// decodeCategoryRequest parses and validates the shared create/update body,
// writing the 400 itself and reporting whether the caller may continue.
func decodeCategoryRequest(w http.ResponseWriter, r *http.Request) (categoryRequest, bool) {
	ctx := r.Context()

	var body categoryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return body, false
	}
	name, msg := normalizeCategoryName(body.Name)
	if msg != "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, msg)
		return body, false
	}
	body.Name = name
	// kb_user_categories.sort_order is a Postgres int (int4). A wider value
	// would fail the write with SQLSTATE 22003 and surface as a 500.
	if body.SortOrder < math.MinInt32 || body.SortOrder > math.MaxInt32 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "sortOrder must be a 32-bit integer")
		return body, false
	}
	return body, true
}

// normalizeCategoryName trims a chip label and returns it, or a non-empty
// 400 message saying why it is unusable. The checks, in order:
//
//   - any control character (Unicode Cc, which includes NUL) is refused.
//     Postgres rejects NUL in text with SQLSTATE 22021, which would be a
//     500; newlines and tabs have no meaning in a one-line chip either.
//   - a name with no visible character is refused as empty. Whitespace and
//     format characters (Unicode Cf — zero-width space, joiners, direction
//     marks) render as nothing, and strings.TrimSpace keeps the Cf ones, so a
//     label of U+200B alone would otherwise be stored as an invisible chip.
//   - the length is counted in runes against the varchar(100) column, so a
//     non-ASCII label is not cut short by its byte length and an over-long
//     one is a 400 rather than SQLSTATE 22001.
func normalizeCategoryName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	visible := false
	for _, c := range name {
		if unicode.IsControl(c) {
			return "", "name must not contain control characters"
		}
		if !unicode.IsSpace(c) && !unicode.Is(unicode.Cf, c) {
			visible = true
		}
	}
	if !visible {
		return "", "name is required"
	}
	if utf8.RuneCountInString(name) > maxCategoryNameLen {
		return "", "name is too long"
	}
	return name, ""
}

// pathUUID reads a path parameter, rejects anything that is not a UUID, and
// returns the canonical form. uuid.Parse also accepts "urn:uuid:…", braced
// and hyphen-less spellings that Postgres's ::uuid cast does not all accept,
// so handing on the raw value could still end in a 500; parsed.String() is
// the lower-case hyphenated form Postgres always takes. A bad id in the path
// is a client error, and for these routes it is indistinguishable from an id
// that does not exist, hence 404.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	parsed, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "category not found")
		return "", false
	}
	return parsed.String(), true
}
