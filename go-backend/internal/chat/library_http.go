package chat

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/usage"
	"github.com/justrag/go-backend/internal/userfiles"
)

// ---------------------------------------------------------------------------
// KB-less library chat (user file library, phase 3 — P3-R4/R5)
//
// POST /api/library/chat answers from the caller's own library files: no KB,
// no retrieval index, no orchestrator. The selected files' parsed text becomes
// the context (BuildLibraryContext), and the answer goes through the shared
// writers in library mode (chatResponseParams.library).
// ---------------------------------------------------------------------------

// maxLibraryChatFiles caps the files one library chat may select (P3-R4).
const maxLibraryChatFiles = 20

// LibraryChatStore is the chat-store surface the library endpoints need on top
// of Store. Satisfied by *PGStore.
type LibraryChatStore interface {
	CreateLibraryChat(ctx context.Context, userID, title string) (*ChatRow, error)
	GetLibraryChats(ctx context.Context, userID string) ([]ChatRow, error)
	GetChatFileRefs(ctx context.Context, chatID string) ([]string, error)
	// GetChatsFileRefs is GetChatFileRefs for many chats in one query.
	GetChatsFileRefs(ctx context.Context, chatIDs []string) (map[string][]string, error)
	// ReplaceChatFileRefs does no ownership check: every id must have been
	// validated with LibraryFileGetter.Get first.
	ReplaceChatFileRefs(ctx context.Context, chatID string, userFileIDs []string) error
}

// LibraryFileGetter resolves a library file owner-scoped. It returns
// userfiles.ErrNotFound for a missing, foreign or malformed id. Satisfied by
// userfiles.Store.
type LibraryFileGetter interface {
	Get(ctx context.Context, ownerID, id string) (*userfiles.UserFile, error)
}

// LibraryTextProvider turns a library file into parsed text. Satisfied by
// *LibraryTextSource; ErrUnparseable marks a parser failure.
type LibraryTextProvider interface {
	Text(ctx context.Context, uf *userfiles.UserFile) (*parser.ParseResult, error)
}

// WithLibraryChat wires the KB-less library chat endpoints. Optional — when
// absent, they answer 503.
func WithLibraryChat(chats LibraryChatStore, files LibraryFileGetter, text LibraryTextProvider) HandlerOption {
	return func(h *Handler) {
		h.libraryChats = chats
		h.libraryFiles = files
		h.libraryText = text
	}
}

func (h *Handler) libraryChatWired() bool {
	return h.libraryChats != nil && h.libraryFiles != nil && h.libraryText != nil
}

// SendLibraryMessage handles POST /api/library/chat[?stream=true].
// Body: {message, chatId?, parentMessageId?, language?, fileIds?: [1..20]}.
// A new chat requires fileIds; an existing chat uses the body's fileIds when
// given (replacing its stored selection), else the stored selection. Every
// rejection (404 / 400) happens before a chat is created, a selection is
// replaced or a usage event is recorded.
//
// In stream mode the SSE stream opens (with a {"stage":"library_prepare"}
// frame) right after the cheap owner/selection checks and BEFORE the files
// are parsed, so a slow server-side parse cannot idle-time-out a proxy. Every
// later rejection is then an SSE {"error": …} frame followed by [DONE] (the
// KB path's post-SSE error shape) instead of a status code.
func (h *Handler) SendLibraryMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.libraryChatWired() {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "library chat is not available")
		return
	}
	streamMode := r.URL.Query().Get("stream") == "true"

	ctx = logctx.Attach(logctx.WithUser(ctx, user.ID))
	ctx, span := observability.Tracer().Start(ctx, "chat.send_library_message")
	defer span.End()
	span.SetAttributes(attribute.Bool("chat.stream", streamMode), attribute.Bool("chat.library", true))
	ctx = h.attachTurnBudget(ctx)

	body, ok := parseLibraryMessage(w, r, user.ID)
	if !ok {
		return
	}
	chat, ok := h.loadLibraryChat(ctx, w, body.ChatID, user.ID)
	if !ok {
		return
	}
	files, ok := h.resolveLibraryFiles(ctx, w, user.ID, chat, body.FileIDs)
	if !ok {
		return
	}
	reply := libraryReply{w: w}
	if streamMode {
		reply.openStream(ctx)
	}
	libFiles, ok := h.loadLibraryTexts(ctx, reply, files)
	if !ok {
		return
	}

	lang := body.Language
	if !supportedLanguages[lang] {
		lang = defaultLanguage
	}
	// A new chat has no history; a parent id from elsewhere is ignored.
	var parentMsgID *string
	var convRows []MessageRow
	if chat != nil {
		parentMsgID = h.parentInChat(ctx, chat.ID, SanitizeParentMessageID(body.ParentMessageID))
		convRows = h.loadConversationRows(ctx, chat.ID, parentMsgID)
	}

	var bufferedTrajectory []map[string]any
	var collectEmit func(map[string]any)
	if streamMode {
		collectEmit = func(d map[string]any) { bufferedTrajectory = append(bufferedTrajectory, d) }
	}
	chatCtx, err := BuildLibraryContext(ctx, h.aiResolver, h.siteConfigReader, LibraryContextParams{
		Files:           libFiles,
		Query:           body.Message,
		Language:        lang,
		CurrentDateLine: SystemPromptDateLine(ctx, h.siteConfigReader, lang),
		Emit:            collectEmit,
	})
	if err != nil {
		writeLibraryContextError(ctx, reply, err)
		return
	}

	chatID, ok := h.commitLibraryChat(ctx, reply, chat, user.ID, body, files)
	if !ok {
		return
	}
	// Usage ledger (P3-R7): KbID "" is stored as NULL; web surface.
	if h.usageRecorder != nil {
		h.usageRecorder.Record(ctx, usage.Event{
			UserID:   user.ID,
			APIKeyID: auth.APIKeyIDFromContext(ctx),
			Surface:  usage.SurfaceWeb,
		})
	}

	userMsg, err := h.resolveTurnUserMessage(ctx, AddMessageParams{
		ChatID:  chatID,
		Role:    "user",
		Content: body.Message,
	}, turnAnchor{ParentMessageID: parentMsgID})
	if err != nil {
		logctx.From(ctx).Error("chat.library: save user message", "error", err, "chat_id", chatID)
		reply.fail(ctx, http.StatusInternalServerError, "failed to save user message")
		return
	}

	rp := chatResponseParams{
		span:               span,
		chatID:             chatID,
		lang:               lang,
		userMessage:        body.Message,
		reasoningLevel:     resolveReasoningLevel(body),
		userMsgID:          userMsg.ID,
		chatCtx:            chatCtx,
		bufferedTrajectory: bufferedTrajectory,
		chatStartTime:      time.Now(),
		history:            h.answerHistory(ctx, convRows),
		library:            true,
	}
	if streamMode {
		h.writeStreamingResponse(ctx, w, rp)
		return
	}
	h.writeJSONResponse(ctx, w, rp)
}

// parseLibraryMessage reuses the KB body validation and adds the library
// rules: no regenerate, at most maxLibraryChatFiles file ids.
func parseLibraryMessage(w http.ResponseWriter, r *http.Request, userID string) (sendMessageRequest, bool) {
	body, ok := parseAndValidateMessage(w, r, userID)
	if !ok {
		return body, false
	}
	if body.RegenerateOfMessageID != "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "regenerate is not supported for library chats")
		return body, false
	}
	if len(body.FileIDs) > maxLibraryChatFiles {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "at most 20 library files can be selected")
		return body, false
	}
	return body, true
}

// loadLibraryChat returns the caller's library chat for chatID, or nil when
// chatID is empty (a new chat). A malformed, missing, foreign or non-library
// chat (including any KB chat) is a 404.
func (h *Handler) loadLibraryChat(ctx context.Context, w http.ResponseWriter, chatID, userID string) (*ChatRow, bool) {
	if chatID == "" {
		return nil, true
	}
	chat, ok := h.ownedLibraryChat(ctx, w, chatID, userID)
	return chat, ok
}

// ownedLibraryChat is the shared owner + type check of the library endpoints.
func (h *Handler) ownedLibraryChat(ctx context.Context, w http.ResponseWriter, chatID, userID string) (*ChatRow, bool) {
	if _, err := uuid.Parse(chatID); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "chat not found")
		return nil, false
	}
	chat, err := h.store.GetChatByID(ctx, chatID)
	if err != nil {
		logctx.From(ctx).Error("chat.library: get chat", "error", err, "chat_id", chatID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch chat")
		return nil, false
	}
	if chat == nil || chat.UserID != userID || chat.Type != "library" || chat.KbID != "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "chat not found")
		return nil, false
	}
	return chat, true
}

// resolveLibraryFiles validates the turn's file selection owner-scoped. Body
// ids must all resolve (else 404, naming no file); duplicates collapse in
// order. Without body ids an existing chat uses its stored refs, silently
// dropping any file deleted since. An empty result is a 400.
func (h *Handler) resolveLibraryFiles(ctx context.Context, w http.ResponseWriter, userID string, chat *ChatRow, bodyIDs []string) ([]*userfiles.UserFile, bool) {
	ids, fromBody := bodyIDs, len(bodyIDs) > 0
	if !fromBody && chat != nil {
		stored, err := h.libraryChats.GetChatFileRefs(ctx, chat.ID)
		if err != nil {
			logctx.From(ctx).Error("chat.library: get file refs", "error", err, "chat_id", chat.ID)
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to load library files")
			return nil, false
		}
		ids = stored
	}
	seen := make(map[string]bool, len(ids))
	files := make([]*userfiles.UserFile, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		uf, err := h.libraryFiles.Get(ctx, userID, id)
		switch {
		case errors.Is(err, userfiles.ErrNotFound) && fromBody:
			httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "file not found")
			return nil, false
		case errors.Is(err, userfiles.ErrNotFound):
			continue // deleted between turns; its ref is cascading away
		case err != nil:
			logctx.From(ctx).Error("chat.library: get library file", "error", err, "user_file_id", id)
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to load library files")
			return nil, false
		}
		files = append(files, uf)
	}
	if len(files) == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "no library files selected")
		return nil, false
	}
	return files, true
}

// libraryReply writes a send-turn rejection: a plain JSON error while the
// response is still unopened, or — once openStream has committed the SSE
// stream — the KB path's post-SSE shape, an {"error": …} frame + [DONE].
type libraryReply struct {
	w         http.ResponseWriter
	streaming bool
}

// openStream commits the SSE response and sends the library_prepare frame,
// so the client sees a byte before the (possibly slow) server-side parse.
func (lr *libraryReply) openStream(ctx context.Context) {
	httputil.EnableSSE(lr.w)
	lr.streaming = true
	writeSSE(ctx, lr.w, map[string]any{"stage": "library_prepare"})
}

func (lr libraryReply) fail(ctx context.Context, status int, msg string) {
	if lr.streaming {
		writeSSE(ctx, lr.w, map[string]string{"error": msg})
		writeSSEDone(ctx, lr.w)
		return
	}
	httputil.WriteErrorCtx(ctx, lr.w, status, msg)
}

// loadLibraryTexts parses every selected file. A parser failure or parse
// timeout is a 400 naming the file (the cause is never echoed); anything else
// is a 500. Parses run detached (see LibraryTextSource.Text), so a file whose
// parse finished is cached even if the client left; the loop itself stops at
// the next file once the request is gone.
func (h *Handler) loadLibraryTexts(ctx context.Context, reply libraryReply, files []*userfiles.UserFile) ([]LibraryFile, bool) {
	out := make([]LibraryFile, 0, len(files))
	for i, uf := range files {
		if err := ctx.Err(); err != nil {
			logctx.From(ctx).Info("chat.library: request ended while reading files", "error", err)
			reply.fail(ctx, http.StatusInternalServerError, "failed to read library file")
			return nil, false
		}
		if reply.streaming {
			writeSSE(ctx, reply.w, map[string]any{"stage": "library_parse", "file": uf.Name, "index": i, "total": len(files)})
		}
		parsed, err := h.libraryText.Text(ctx, uf)
		switch {
		case errors.Is(err, ErrUnparseable):
			reply.fail(ctx, http.StatusBadRequest, "the file \""+uf.Name+"\" cannot be read as text")
			return nil, false
		case errors.Is(err, ErrLibraryParseTimeout):
			reply.fail(ctx, http.StatusBadRequest, "the file \""+uf.Name+"\" took too long to read")
			return nil, false
		case err != nil:
			logctx.From(ctx).Error("chat.library: read library file", "error", err, "user_file_id", uf.ID)
			reply.fail(ctx, http.StatusInternalServerError, "failed to read library file")
			return nil, false
		}
		out = append(out, LibraryFile{UserFileID: uf.ID, Name: uf.Name, Parsed: parsed})
	}
	return out, true
}

// writeLibraryContextError maps BuildLibraryContext failures: both budget and
// no-text verdicts are user-facing 400s, everything else a 500.
func writeLibraryContextError(ctx context.Context, reply libraryReply, err error) {
	var tooLarge *ErrLibraryTooLarge
	switch {
	case errors.As(err, &tooLarge):
		reply.fail(ctx, http.StatusBadRequest, tooLarge.Error())
	case errors.Is(err, ErrLibraryNoText):
		reply.fail(ctx, http.StatusBadRequest, "the selected files contain no text")
	default:
		logctx.From(ctx).Error("chat.library: build context", "error", err)
		reply.fail(ctx, http.StatusInternalServerError, "failed to prepare context")
	}
}

// commitLibraryChat creates the chat for a new turn (titled from the first 50
// runes of the message) and persists a body file selection as the chat's refs.
// Every id in files was validated owner-scoped by resolveLibraryFiles.
func (h *Handler) commitLibraryChat(ctx context.Context, reply libraryReply, chat *ChatRow, userID string, body sendMessageRequest, files []*userfiles.UserFile) (string, bool) {
	created := chat == nil
	if created {
		title := body.Message
		if runes := []rune(title); len(runes) > 50 {
			title = string(runes[:50])
		}
		newChat, err := h.libraryChats.CreateLibraryChat(ctx, userID, title)
		if err != nil {
			logctx.From(ctx).Error("chat.library: create chat", "error", err)
			reply.fail(ctx, http.StatusInternalServerError, "failed to create chat")
			return "", false
		}
		chat = newChat
	}
	if len(body.FileIDs) == 0 {
		return chat.ID, true
	}
	ids := make([]string, len(files))
	for i, uf := range files {
		ids[i] = uf.ID
	}
	err := h.libraryChats.ReplaceChatFileRefs(ctx, chat.ID, ids)
	if err == nil {
		return chat.ID, true
	}
	// A failed selection must not leave an empty new chat behind.
	if created {
		if derr := h.store.DeleteChat(ctx, chat.ID); derr != nil {
			logctx.From(ctx).Warn("chat.library: remove chat after failed refs", "error", derr, "chat_id", chat.ID)
		}
	}
	if errors.Is(err, ErrChatFileRefGone) {
		// A file was deleted after validation: same answer as a missing id.
		reply.fail(ctx, http.StatusNotFound, "file not found")
		return "", false
	}
	logctx.From(ctx).Error("chat.library: replace file refs", "error", err, "chat_id", chat.ID)
	reply.fail(ctx, http.StatusInternalServerError, "failed to save library files")
	return "", false
}

// libraryChatItem is the list/get shape of a library chat (P3-R4).
type libraryChatItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	FileIDs   []string  `json:"fileIds"`
}

func (h *Handler) libraryChatItem(ctx context.Context, c ChatRow) (libraryChatItem, error) {
	refs, err := h.libraryChats.GetChatFileRefs(ctx, c.ID)
	if err != nil {
		return libraryChatItem{}, err
	}
	return newLibraryChatItem(c, refs), nil
}

func newLibraryChatItem(c ChatRow, refs []string) libraryChatItem {
	if refs == nil {
		refs = []string{}
	}
	return libraryChatItem{ID: c.ID, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, FileIDs: refs}
}

// ListLibraryChats handles GET /api/library/chats → {items: [...]}, the
// caller's library chats only, newest activity first.
func (h *Handler) ListLibraryChats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.libraryChatWired() {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "library chat is not available")
		return
	}
	chats, err := h.libraryChats.GetLibraryChats(ctx, user.ID)
	if err != nil {
		logctx.From(ctx).Error("chat.library: list chats", "error", err, "user_id", user.ID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch chats")
		return
	}
	ids := make([]string, len(chats))
	for i, c := range chats {
		ids[i] = c.ID
	}
	// One query for every chat's selection (not one per chat).
	refs, err := h.libraryChats.GetChatsFileRefs(ctx, ids)
	if err != nil {
		logctx.From(ctx).Error("chat.library: list file refs", "error", err, "user_id", user.ID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch chats")
		return
	}
	items := make([]libraryChatItem, 0, len(chats))
	for _, c := range chats {
		items = append(items, newLibraryChatItem(c, refs[c.ID]))
	}
	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"items": items})
}

// GetLibraryChat handles GET /api/library/chats/{id}: one library chat in the
// list item shape; 404 for a malformed, missing, foreign or non-library chat.
func (h *Handler) GetLibraryChat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !h.libraryChatWired() {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "library chat is not available")
		return
	}
	chat, ok := h.ownedLibraryChat(ctx, w, r.PathValue("id"), user.ID)
	if !ok {
		return
	}
	item, err := h.libraryChatItem(ctx, *chat)
	if err != nil {
		logctx.From(ctx).Error("chat.library: get file refs", "error", err, "chat_id", chat.ID)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to fetch chat")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, item)
}
