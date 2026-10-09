package chat

import (
	"context"
	"fmt"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/agui"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/usage"
)

// MessageEvent is the CUSTOM event naming the persisted AI message of a
// completed agent-chat turn: {"aiMessageId", "chatId"}.
const MessageEvent = "justrag.message.v1"

// agentChatStore is the slice of *PGStore the agent-chat hooks use.
type agentChatStore interface {
	GetChatByID(ctx context.Context, chatID string) (*ChatRow, error)
	CreateChatWithID(ctx context.Context, id, kbID, userID, title string) (*ChatRow, error)
	DeleteChat(ctx context.Context, chatID string) error
	LastMessageID(ctx context.Context, chatID string) (*string, error)
	AddMessage(ctx context.Context, p AddMessageParams) (*MessageRow, error)
}

// agentChatHooks maps AG-UI threads onto chats (threadId = chat id) and
// persists a turn's messages. One instance per request: it holds the
// request's retriever (whose sources the AI message carries) and the state
// carried from TurnStarted to TurnFinished.
type agentChatHooks struct {
	store     agentChatStore
	usage     usage.Recorder // may be nil
	retriever *AgentRetriever

	// newThread: ResolveThread minted the id; TurnStarted creates the chat
	// (Ruling P2-R9 — no chat exists for a request that never starts).
	newThread bool
	// userMsgID is the user message TurnStarted wrote ("" on a resume).
	userMsgID string
	// threadID and parentMsgID anchor the floor retrieval's condenser:
	// the resolved chat and the message this turn's question replies to
	// (nil on a new thread or a resume — nothing to condense against).
	threadID    string
	parentMsgID *string
}

var _ agui.TurnHooks = (*agentChatHooks)(nil)

// ResolveThread returns a fresh id for a new thread without creating
// anything, and otherwise the canonical id of a chat the caller owns in
// this KB. Any other id — including a fresh id from an earlier request that
// never started — is agui.ErrThreadNotFound.
func (h *agentChatHooks) ResolveThread(ctx context.Context, sc adkbridge.Scope, threadID, _ string) (string, error) {
	if threadID == "" {
		h.newThread = true
		return uuid.NewString(), nil
	}
	u, err := uuid.Parse(threadID)
	if err != nil {
		return "", agui.ErrThreadNotFound
	}
	id := u.String()
	c, err := h.store.GetChatByID(ctx, id)
	if err != nil {
		return "", err
	}
	if c == nil || c.UserID != sc.UserID || c.KbID != sc.KBID {
		return "", agui.ErrThreadNotFound
	}
	h.threadID = id
	return id, nil
}

// TurnStarted creates a new thread's chat, stores the user message and
// records the turn in the usage ledger. A resume (userText "") does none of
// that: its question is already stored and its turn already counted.
func (h *agentChatHooks) TurnStarted(ctx context.Context, sc adkbridge.Scope, threadID, _ string, userText string) error {
	if userText == "" {
		// A resume continues a turn already counted (Ruling P2-R14).
		return nil
	}
	if err := h.storeUserMessage(ctx, sc, threadID, userText); err != nil {
		return err
	}
	if h.usage != nil {
		h.usage.Record(ctx, usage.Event{
			KbID:     sc.KBID,
			UserID:   sc.UserID,
			APIKeyID: auth.APIKeyIDFromContext(ctx),
			Surface:  usage.SurfaceWeb,
		})
	}
	return nil
}

func (h *agentChatHooks) storeUserMessage(ctx context.Context, sc adkbridge.Scope, threadID, userText string) error {
	var parent *string
	if h.newThread {
		if _, err := h.store.CreateChatWithID(ctx, threadID, sc.KBID, sc.UserID, chatTitle(userText)); err != nil {
			return err
		}
	} else {
		var err error
		if parent, err = h.store.LastMessageID(ctx, threadID); err != nil {
			return err
		}
	}
	msg, err := h.store.AddMessage(ctx, AddMessageParams{ChatID: threadID, Role: "user", Content: userText, ParentMessageID: parent})
	if err != nil {
		if h.newThread {
			// No empty chat for a turn that never ran.
			if derr := h.store.DeleteChat(context.WithoutCancel(ctx), threadID); derr != nil {
				logctx.From(ctx).Warn("agentchat: delete chat after failed user message", "chat_id", threadID, "error", derr)
			}
		}
		return err
	}
	h.userMsgID = msg.ID
	h.threadID, h.parentMsgID = threadID, parent
	return nil
}

// TurnFinished stores the answer of a completed turn and names it to the
// client. An interrupted, failed or cancelled turn stores nothing more:
// its user message is already stored.
func (h *agentChatHooks) TurnFinished(ctx context.Context, _ adkbridge.Scope, threadID string, out agui.TurnOutput) ([]events.Event, error) {
	if out.Status != adkbridge.RunCompleted || out.Text == "" {
		return nil, nil
	}
	parent, err := h.answerParent(ctx, threadID)
	if err != nil {
		return nil, err
	}
	var reasoning *string
	if out.Reasoning != "" {
		reasoning = &out.Reasoning
	}
	var sources []ChatSource
	if h.retriever != nil {
		sources = h.retriever.Sources()
	}
	msg, err := h.store.AddMessage(ctx, AddMessageParams{
		ChatID:          threadID,
		Role:            "ai",
		Content:         out.Text,
		Sources:         sources,
		Reasoning:       reasoning,
		ParentMessageID: parent,
	})
	if err != nil {
		return nil, fmt.Errorf("agentchat: store answer: %w", err)
	}
	return []events.Event{events.NewCustomEvent(MessageEvent,
		events.WithValue(map[string]any{"aiMessageId": msg.ID, "chatId": threadID}))}, nil
}

// answerParent is the user message the answer replies to: this request's,
// or — on a resume — the chat's newest message (the paused turn stored
// nothing after its question).
func (h *agentChatHooks) answerParent(ctx context.Context, threadID string) (*string, error) {
	if h.userMsgID != "" {
		id := h.userMsgID
		return &id, nil
	}
	return h.store.LastMessageID(ctx, threadID)
}
