package jobs

// FileProcessingPayload is the JSON payload for a file-processing job.
type FileProcessingPayload struct {
	FileID       string `json:"fileId"`
	KbID         string `json:"kbId"`
	FilePath     string `json:"filePath"`
	OriginalName string `json:"originalName"`
	MimeType     string `json:"mimetype"`
}

// TextProcessingPayload is the JSON payload for a text-processing job.
// The content is inline text that gets written to a temp file before processing.
type TextProcessingPayload struct {
	FileID       string `json:"fileId"`
	KbID         string `json:"kbId"`
	Content      string `json:"content"` // inline text content
	OriginalName string `json:"originalName"`
	MimeType     string `json:"mimetype"`
}

// URLProcessingPayload is the JSON payload for a url-processing job. The file
// retrieves it and passes it to the processor.
type URLProcessingPayload struct {
	FileID       string `json:"fileId"`
	KbID         string `json:"kbId"`
	FilePath     string `json:"filePath"` // storage path
	OriginalName string `json:"originalName"`
	MimeType     string `json:"mimetype"`
}

// RSSPollPayload is the task payload for an RSS feed poll job.
type RSSPollPayload struct {
	FeedID string `json:"feedId"`
}

// ConfluenceSyncPayload is the task payload for a Confluence source sync job.
type ConfluenceSyncPayload struct {
	SourceID string `json:"sourceId"`
}

// RAGASSamplePayload is the JSON payload for a Phase 3 §G RAGAS sample
// task. Carries everything the worker needs to invoke eval.Judge.Evaluate
// without re-running search: the user's question, the AI's answer, and
// the retrieved chunks (file IDs + content text). The contents are
// inlined rather than refetched by chunk_id because (a) chunk content
// can change between sample-time and worker-time if the file is
// re-uploaded, and (b) Asynq payloads in Redis handle MB-scale values
// fine for the typical 10-chunk × ~2KB envelope.
type RAGASSamplePayload struct {
	KbID     string             `json:"kbId"`
	Language string             `json:"language"` // "de" | "en" | ...
	Question string             `json:"question"` // user message text (post-condense)
	Answer   string             `json:"answer"`   // AI response text (final, post-stream)
	Chunks   []RAGASSampleChunk `json:"chunks"`   // empty slice = retrieval returned 0 chunks
	// MessageID is the chat message id (uuid). Carried for log
	// correlation only — the worker doesn't query the messages table.
	MessageID string `json:"messageId"`
}

// RAGASSampleChunk mirrors the minimum shape eval.RetrievedChunk expects
// plus the content text needed for the context-precision prompt.
type RAGASSampleChunk struct {
	FileID  string  `json:"fileId"`
	Score   float64 `json:"score"`
	Content string  `json:"content"`
}

// ReEmbedUserMemoryPayload is the payload for a per-user memory re-embed
// job. The handler loads the user's live memory rows and recomputes their
// embeddings with the active embedder.
type ReEmbedUserMemoryPayload struct {
	UserID string `json:"userId"`
}
