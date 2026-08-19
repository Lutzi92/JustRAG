package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// ---------------------------------------------------------------------------
// Regenerate: a second answer under the SAME question
// ---------------------------------------------------------------------------
//
// "Antwort neu generieren" used to re-send the question through the normal
// send path, which inserted a *second* user row. On a mid-conversation turn
// that at least produced a sibling branch; on the first turn of a chat the
// client had no parent to branch from and the copy was appended linearly, so
// the user saw their own prompt twice in one thread.
//
// A regenerate therefore does not carry a question of its own: it names the AI
// message to replace, and the new answer is hung under that message's existing
// user row as a sibling. The question is stored exactly once and the
// `‹1/2›` branch switcher lands on the answers, where it belongs.

// regenerateTurn is a resolved regenerate request: everything the answer path
// needs in order to run a turn without inserting a question.
type regenerateTurn struct {
	// UserMsg is the existing user row the new answer hangs under.
	UserMsg MessageRow
	// HistoryParentID anchors the conversation-history lookup at the turn
	// *before* the question, so the answer being replaced is not fed back in
	// as context. nil means the question is the start of the chat.
	HistoryParentID *string
	// HistoryRows are the turns strictly above the question, oldest first —
	// carried here rather than re-loaded, because loadConversationRows and
	// CondenseFollowUp both read a nil parent as "no anchor given" and fall
	// back to the WHOLE chat. For a regenerate at the root of a chat that
	// fallback would hand the answer being replaced back to the answer LLM.
	HistoryRows []MessageRow
	// Question is the stored question text. It is authoritative: the client's
	// own message field is discarded, so a regenerate cannot smuggle in a
	// different question than the one the user sees above the answer.
	Question string
}

// errNoRegenerateTarget reports a regenerate request that does not name a
// usable AI message in this chat. Callers translate it into a 400.
var errNoRegenerateTarget = errors.New("regenerate: no such AI message in this chat")

// resolveRegenerateTurn picks, out of the ancestor chain of the AI message
// being regenerated, the user message the new answer must hang under.
//
// rows is what Store.GetMessageAncestors returns for (aiMessageID, chatID):
// the target plus all of its ancestors, already scoped to the chat. A message
// belonging to another chat therefore arrives here as an empty chain and is
// rejected rather than silently degrading into a normal turn.
func resolveRegenerateTurn(rows []MessageRow, aiMessageID string) (*regenerateTurn, error) {
	byID := make(map[string]MessageRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	target, ok := byID[aiMessageID]
	if !ok {
		return nil, errNoRegenerateTarget
	}
	if target.Role != "ai" {
		return nil, fmt.Errorf("regenerate: message %s is a %s message, not an answer", aiMessageID, target.Role)
	}
	if target.ParentMessageID == nil {
		return nil, fmt.Errorf("regenerate: answer %s has no question to hang under", aiMessageID)
	}

	userMsg, ok := byID[*target.ParentMessageID]
	if !ok {
		return nil, fmt.Errorf("regenerate: question %s of answer %s is missing", *target.ParentMessageID, aiMessageID)
	}
	if userMsg.Role != "user" {
		return nil, fmt.Errorf("regenerate: parent %s of answer %s is a %s message", userMsg.ID, aiMessageID, userMsg.Role)
	}

	history := make([]MessageRow, 0, len(rows))
	for _, r := range rows {
		if r.ID == target.ID || r.ID == userMsg.ID {
			continue
		}
		history = append(history, r)
	}

	return &regenerateTurn{
		UserMsg:         userMsg,
		HistoryParentID: userMsg.ParentMessageID,
		HistoryRows:     history,
		Question:        userMsg.Content,
	}, nil
}

// turnAnchor says where this turn's messages attach in the message tree. It
// travels the answer paths in place of a bare parent id so that "insert the
// question under this parent" and "reuse an existing question" cannot be
// confused at a call site.
type turnAnchor struct {
	// ParentMessageID is the parent of the question this turn inserts.
	ParentMessageID *string
	// Regenerate, when non-nil, means the question already exists: the new
	// answer hangs under it as a sibling and no question row is written.
	Regenerate *regenerateTurn
}

// resolveTurnUserMessage returns the user message this turn's answer hangs
// under. It is the single seam every answer path inserts questions through:
// a regenerate reuses the stored row, any other turn inserts a new one.
func (h *Handler) resolveTurnUserMessage(ctx context.Context, p AddMessageParams, a turnAnchor) (*MessageRow, error) {
	if a.Regenerate != nil {
		msg := a.Regenerate.UserMsg
		return &msg, nil
	}
	p.ParentMessageID = a.ParentMessageID
	return h.store.AddMessage(ctx, p)
}

// resolveRegenerate loads and validates the answer named by
// body.RegenerateOfMessageID and rewrites the request to re-answer its stored
// question. Returns false when it has already written an error response.
func (h *Handler) resolveRegenerate(ctx context.Context, w http.ResponseWriter, chatID string, body *sendMessageRequest) (*regenerateTurn, bool) {
	// A client placeholder ("temp-ai-…") must be rejected outright rather than
	// treated as absent: falling through to a normal turn is exactly the
	// duplicate-question behaviour this path replaces.
	id := SanitizeParentMessageID(body.RegenerateOfMessageID)
	if id == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "regenerateOfMessageId must be a message id")
		return nil, false
	}

	// Scoped by chat_id, so an answer from another conversation comes back as
	// an empty chain and is rejected by resolveRegenerateTurn below.
	rows, err := h.store.GetMessageAncestors(ctx, *id, chatID)
	if err != nil {
		logctx.From(ctx).Error("chat.regenerate: load ancestors", "error", err, "chat_id", chatID, "message_id", *id)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to load the answer to regenerate")
		return nil, false
	}

	regen, err := resolveRegenerateTurn(rows, *id)
	if err != nil {
		logctx.From(ctx).Warn("chat.regenerate: rejected", "error", err, "chat_id", chatID, "message_id", *id)
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "cannot regenerate this message")
		return nil, false
	}

	// The stored question wins over whatever the client sent, and the query
	// enhancer stays out: the user did not retype anything, so rewriting their
	// question here would answer something they never asked.
	body.Message = regen.Question
	body.Enhance = ""
	return regen, true
}
