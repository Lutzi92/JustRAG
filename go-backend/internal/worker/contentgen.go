package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/contentgen"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// PodcastDeps holds dependencies for the podcast generation worker.
//
// TTSHTTPClient is the long-timeout HTTP client used for TTS calls
// (constructed via ai.NewTTSHTTPClient at worker startup). Required —
// GenerateTTS rejects a nil client.
type PodcastDeps struct {
	AIResolver    *ai.ConfigResolver
	SearchSvc     vector.Searcher
	Store         *gencontent.PGStore
	Redis         *redis.Client
	TTSHTTPClient *http.Client
}

var reUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

func sanitize(s string) string { return reUnsafe.ReplaceAllString(s, "_") }

// NewPodcastHandler returns an Asynq handler for podcast generation
// (script + TTS audio).
func NewPodcastHandler(deps PodcastDeps) func(ctx context.Context, task *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload contentgen.PodcastJobPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("podcast: unmarshal payload: %w", err)
		}

		jobID := task.ResultWriter().TaskID()
		progressKey := contentgen.PodcastProgressKey(jobID)

		slog.Info("podcast generation started", "jobId", jobID, "kbId", payload.KbID, "topic", payload.Topic)

		// Helper to write progress to Redis. Includes userId so the
		// status endpoint can verify ownership before returning data.
		setProgress := func(state string, progress map[string]any, errMsg string) {
			data := map[string]any{"state": state, "progress": progress, "userId": payload.UserID}
			if errMsg != "" {
				data["error"] = errMsg
			}
			b, _ := json.Marshal(data)
			deps.Redis.Set(ctx, progressKey, string(b), 30*time.Minute)
		}

		setProgress("active", map[string]any{"step": "script", "message": "Generating script..."}, "")

		// Step 1: Generate podcast script.
		contextStr, err := searchContext(ctx, deps.SearchSvc, payload.KbID, payload.Topic, 25)
		if err != nil {
			setProgress("failed", nil, "failed to retrieve context")
			return fmt.Errorf("podcast: get context: %w", err)
		}

		// Hard-pin English: TTS only produces good speech in English, so we
		// generate the script in English regardless of payload.Language. The
		// HTTP handler also pins this, but we duplicate the guard here to
		// stay correct if a job is enqueued by a future code path.
		lang := "en"
		_ = payload.Language

		prompt := fmt.Sprintf("Topic: %s\n\nContext:\n%s\n\nWrite a podcast script in English based on the context above. Even if the context is in another language, the script MUST be written entirely in English.", payload.Topic, contextStr)
		script, err := ai.GenerateCompletion(ctx, deps.AIResolver, prompt, prompts.PodcastSystemPrompt(lang), payload.KbID, false)
		if err != nil {
			setProgress("failed", nil, "script generation failed")
			return fmt.Errorf("podcast: generate script: %w", err)
		}

		// Step 2: Generate TTS audio.
		var audioPath string
		segments := ai.ParsePodcastSegments(script.Content)

		if len(segments) > 0 {
			hostVoice, expertVoice, _ := ai.GetTTSVoices(ctx, deps.AIResolver, payload.KbID)

			var audioChunks [][]byte
			for i, seg := range segments {
				setProgress("active", map[string]any{
					"step":    "audio",
					"current": i + 1,
					"total":   len(segments),
					"message": fmt.Sprintf("Generating audio (%d/%d)...", i+1, len(segments)),
				}, "")

				voice := hostVoice
				if seg.Speaker == "BOB" {
					voice = expertVoice
				}

				data, err := ai.GenerateTTS(ctx, deps.AIResolver, deps.TTSHTTPClient, seg.Text, payload.KbID, voice)
				if err != nil {
					slog.Warn("podcast: TTS failed for segment, skipping", "segment", i, "error", err)
					continue
				}
				audioChunks = append(audioChunks, data)
			}

			if len(audioChunks) > 0 {
				// Concatenate MP3 chunks.
				var total int
				for _, c := range audioChunks {
					total += len(c)
				}
				combined := make([]byte, 0, total)
				for _, c := range audioChunks {
					combined = append(combined, c...)
				}

				// Write to disk.
				safeUser := sanitize(payload.Username)
				if safeUser == "" {
					safeUser = "anonymous"
				}
				safeTopic := sanitize(payload.Topic)
				if len(safeTopic) > 50 {
					safeTopic = safeTopic[:50]
				}
				if safeTopic == "" {
					safeTopic = "Untitled"
				}

				fileName := fmt.Sprintf("Podcast_%s_%d.mp3", safeTopic, time.Now().UnixMilli())
				dir := filepath.Join("data", safeUser, payload.KbID, "generated")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					slog.Error("podcast: mkdir failed", "error", err, "dir", dir)
				} else {
					filePath := filepath.Join(dir, fileName)
					if err := os.WriteFile(filePath, combined, 0o644); err != nil {
						slog.Error("podcast: write audio failed", "error", err, "path", filePath)
					} else {
						audioPath = filePath
					}
				}
			}
		} else {
			slog.Warn("podcast: no segments parsed from script")
		}

		// Step 3: Save to database.
		contentVal := map[string]any{
			"script": script.Content,
		}
		if audioPath != "" {
			contentVal["filePath"] = audioPath
		}

		title := fmt.Sprintf("Podcast: %s", payload.Topic)
		_, err = deps.Store.CreateGeneratedContent(ctx, payload.KbID, payload.UserID, "podcast", title, contentVal)
		if err != nil {
			setProgress("failed", nil, "failed to save podcast")
			return fmt.Errorf("podcast: save content: %w", err)
		}

		setProgress("completed", map[string]any{"step": "done", "message": "Done"}, "")
		slog.Info("podcast generation completed", "jobId", jobID, "kbId", payload.KbID, "hasAudio", audioPath != "")
		return nil
	}
}

// searchContext searches the KB for relevant chunks and builds a context string.
func searchContext(ctx context.Context, svc vector.Searcher, kbID, query string, limit int) (string, error) {
	result, err := svc.Search(ctx, kbID, query, limit, vector.SearchOptions{})
	if err != nil {
		return "", err
	}
	if len(result.Chunks) == 0 {
		return "", nil
	}
	var sb strings.Builder
	for i, chunk := range result.Chunks {
		fmt.Fprintf(&sb, "[%d] [Source: %s]\n%s\n\n---\n\n", i+1, chunk.FileName, chunk.Content)
	}
	return sb.String(), nil
}
