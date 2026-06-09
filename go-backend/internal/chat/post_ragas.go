package chat

import (
	"context"
	"encoding/json"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
)

// enqueueRAGASSample serializes the chat context into a RAGASSamplePayload
// and enqueues an asynq task on QueueBatch. Failures are logged at warn
// level and dropped — sampling is a best-effort statistical signal; we
// never want a Redis hiccup to surface as a user-visible error.
func (h *Handler) enqueueRAGASSample(
	ctx context.Context,
	kbID, lang, msgID, userMessage, aiResponse string,
	sources []ChatSource,
) {
	chunks := make([]jobs.RAGASSampleChunk, 0, len(sources))
	for _, s := range sources {
		chunks = append(chunks, jobs.RAGASSampleChunk{
			FileID:  s.FileID,
			Score:   s.Score,
			Content: s.Content,
		})
	}
	payload := jobs.RAGASSamplePayload{
		KbID:      kbID,
		Language:  lang,
		Question:  userMessage,
		Answer:    aiResponse,
		Chunks:    chunks,
		MessageID: msgID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logctx.From(ctx).Warn("ragas_sample: marshal failed", "error", err)
		return
	}
	if _, err := h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeRAGASSample, body),
		asynq.Queue(jobs.QueueBatch),
		asynq.MaxRetry(1),
	); err != nil {
		logctx.From(ctx).Warn("ragas_sample: enqueue failed", "error", err)
	}
}
