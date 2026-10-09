package globalsearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// statusClientClosedRequest is nginx's non-standard 499, recorded when the
// client cancels a search before it completes.
const statusClientClosedRequest = 499

// Handler serves GET /api/search.
//
// Authentication only, like GET /api/kb and GET /api/kb/catalog: there is no
// KB in the path for a role middleware to gate on. Visibility is enforced in
// the store's SQL (kbaccess.VisibleKBsCTE), per row, for every group —
// including the optional kb_id scope. That scope answers 404 for a topic the
// caller cannot see as well as for one that does not exist, so this route
// cannot confirm that a hidden topic exists. Note that this differs from
// kbaccess.RequireKBRole, which answers 404 only for a missing KB and 403 for
// a hidden one: here the KB id is a filter in the query string, not the
// resource in the path.
type Handler struct {
	store Store
}

// NewHandler creates a Handler over store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// Search handles GET /api/search?q=<text>[&kb_id=<uuid>][&limit=<n>].
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	params := r.URL.Query()

	raw := params.Get("q")
	// Postgres rejects both in a text parameter (invalid byte sequence for
	// UTF8; NUL is not allowed in text at all), which would surface as a 500.
	// Query decoding does not catch either: %FF and %00 decode to those bytes.
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "q must be valid UTF-8 without NUL characters")
		return
	}
	text := strings.TrimSpace(raw)
	switch n := utf8.RuneCountInString(text); {
	case n < MinQueryLen:
		// Never a full listing: an empty or too-short query is a client
		// error, not "match everything".
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			fmt.Sprintf("q must be at least %d characters", MinQueryLen))
		return
	case n > MaxQueryLen:
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			fmt.Sprintf("q must be at most %d characters", MaxQueryLen))
		return
	}

	limit := DefaultLimit
	if raw := params.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		// A positive integer too large for int is still a positive integer:
		// Atoi reports ErrRange and returns math.MaxInt, which the clamp
		// below turns into MaxLimit. A negative one (math.MinInt) stays a 400.
		if errors.Is(err, strconv.ErrRange) && n > 0 {
			err = nil
		}
		if err != nil || n < 1 {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		// Above the cap is clamped, not rejected: asking for "as many as you
		// allow" is a reasonable request, and the cap is the answer to it.
		limit = min(n, MaxLimit)
	}

	var kbID string
	if rawID := params.Get("kb_id"); rawID != "" {
		// A malformed id would otherwise reach $3::uuid and surface as a 500.
		// It is answered exactly like an id that does not exist.
		parsed, err := uuid.Parse(rawID)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
			return
		}
		// Canonical form: uuid.Parse also accepts urn:uuid: and braced
		// spellings, which Postgres' uuid input does not all accept.
		kbID = parsed.String()
	}

	resp, err := h.store.Search(ctx, Caller{UserID: user.ID, SysRole: user.Role},
		Query{Text: text, KBID: kbID, Limit: limit})
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
		return
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		// The client went away (closed the dropdown, typed the next
		// character): pgx aborts the query with the request context. Not a
		// server error, so no ERROR log and no 500. Nobody reads the
		// response; the status is only for the access log and metrics, and
		// 499 is the established "client closed request" code (nginx).
		// A request timeout is not this case: it cancels with
		// context.DeadlineExceeded and stays a 500.
		logctx.From(ctx).DebugContext(ctx, "search: client canceled the request", "error", err)
		w.WriteHeader(statusClientClosedRequest)
		return
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to search: %w", err))
		return
	}

	// [] rather than null: the dropdown maps over each group directly.
	if resp.Topics == nil {
		resp.Topics = []TopicHit{}
	}
	if resp.Sources == nil {
		resp.Sources = []SourceHit{}
	}
	if resp.Chats == nil {
		resp.Chats = []ChatHit{}
	}
	if resp.Messages == nil {
		resp.Messages = []MessageHit{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, resp)
}
