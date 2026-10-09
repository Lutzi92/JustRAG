package files

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/userfiles"
)

const maxAdoptIDs = 100

// FileAdopter is the userfiles surface POST /api/kb/{id}/files/adopt needs.
type FileAdopter interface {
	Adopt(ctx context.Context, kbID, callerID string, fileIDs []string) (*userfiles.AdoptResult, error)
}

// SetAdopter enables AdoptLegacy. When nil the endpoint answers 404.
func (h *Handler) SetAdopter(a FileAdopter) { h.adopter = a }

type adoptRequest struct {
	FileIDs []string `json:"fileIds"`
}

// canAdopt is the exact authorization rule of the adopt endpoint: the owner
// of a private KB (superadmin resolves to owner), or a system admin /
// superadmin on a public KB. A mere KB admin is not enough: adoption moves
// the blob into the uploader's private library namespace.
func canAdopt(access *kbaccess.KBAccessResult, sysRole string) bool {
	if access == nil || access.KB == nil {
		return false
	}
	if access.KB.IsGlobal {
		return sysRole == auth.RoleAdmin || sysRole == auth.RoleSuperAdmin
	}
	return access.Role == kbaccess.RoleOwner
}

// AdoptLegacy handles POST /api/kb/{id}/files/adopt: it moves legacy KB
// uploads into their uploader's library without touching the index.
// Per-file problems are reported in "skipped", never as an HTTP error.
func (h *Handler) AdoptLegacy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "Authentication required")
		return
	}
	if h.adopter == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "Not found")
		return
	}
	access := kbaccess.AccessFromContext(ctx)
	if !canAdopt(access, user.Role) {
		httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "Insufficient permissions")
		return
	}

	var req adoptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil ||
		len(req.FileIDs) < 1 || len(req.FileIDs) > maxAdoptIDs {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "fileIds must contain 1-100 ids")
		return
	}

	res, err := h.adopter.Adopt(ctx, access.KB.ID, user.ID, req.FileIDs)
	if err != nil {
		logctx.From(ctx).Error("adopt: failed", "kbId", access.KB.ID, "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, res)
}
