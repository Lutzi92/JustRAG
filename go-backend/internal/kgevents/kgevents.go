// Package kgevents carries the knowledge-graph live-update channel: the
// ingestion processor and the file-delete handler publish "graph_changed" /
// "status" events to a per-KB Redis channel, and the mindmap SSE endpoint
// (internal/kggraph) relays them to the browser. Kept separate so both the
// producer side (processor, files) and the relay side depend on it without
// importing each other.
package kgevents

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/logctx"
)

// Channel is the Redis pub/sub channel for a KB's mindmap updates.
func Channel(kbID string) string { return "kg:" + kbID }

// event is one SSE frame payload. Processing is only meaningful when
// Type == "status"; the frontend ignores it for "graph_changed".
type event struct {
	Type       string `json:"type"`
	Processing bool   `json:"processing"`
}

// Publisher publishes mindmap update events. nil-safe: a nil Publisher (or one
// with a nil client) is a silent no-op, so callers can leave it unset in tests.
type Publisher struct {
	rdb *redis.Client
}

// NewPublisher builds a Publisher over the given Redis client.
func NewPublisher(rdb *redis.Client) *Publisher { return &Publisher{rdb: rdb} }

// PublishGraphChanged tells subscribers the KB's graph data changed and they
// should re-fetch it.
func (p *Publisher) PublishGraphChanged(ctx context.Context, kbID string) {
	p.publish(ctx, kbID, event{Type: "graph_changed"})
}

// PublishStatus tells subscribers whether the KB is still building its graph.
func (p *Publisher) PublishStatus(ctx context.Context, kbID string, processing bool) {
	p.publish(ctx, kbID, event{Type: "status", Processing: processing})
}

func (p *Publisher) publish(ctx context.Context, kbID string, e event) {
	if p == nil || p.rdb == nil || kbID == "" {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := p.rdb.Publish(ctx, Channel(kbID), b).Err(); err != nil {
		// Best-effort: the frontend still has its initial fetch; a missed
		// event just means no live refresh until the user re-opens the tab.
		logctx.From(ctx).Debug("kgevents: publish failed", "kbId", kbID, "type", e.Type, "error", err)
	}
}

// GraphDeleter removes one file's KG contribution. Satisfied by *kg.PgStore.
type GraphDeleter interface {
	DeleteKGForFile(ctx context.Context, kbID, fileID string) error
}

// FileHook bundles the per-file KG cleanup + the graph_changed publish so the
// file-delete handler needs only a single optional dependency.
type FileHook struct {
	pub *Publisher
	del GraphDeleter
}

// NewFileHook builds a FileHook from a Publisher and a GraphDeleter.
func NewFileHook(pub *Publisher, del GraphDeleter) *FileHook {
	return &FileHook{pub: pub, del: del}
}

// OnFileDeleted removes the file's KG contribution, then notifies subscribers.
// Best-effort: a delete error is logged and the publish still fires (the
// frontend re-fetch reflects whatever the DB now holds).
func (h *FileHook) OnFileDeleted(ctx context.Context, kbID, fileID string) {
	if h == nil {
		return
	}
	if h.del != nil {
		if err := h.del.DeleteKGForFile(ctx, kbID, fileID); err != nil {
			logctx.From(ctx).Warn("kgevents: delete KG for file failed", "kbId", kbID, "fileId", fileID, "error", err)
		}
	}
	if h.pub != nil {
		h.pub.PublishGraphChanged(ctx, kbID)
	}
}
