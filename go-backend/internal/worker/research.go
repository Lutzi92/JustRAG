package worker

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/research"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// ResearchStore is the database interface needed by the research worker handler.
// GetSiteConfigValue is required so the worker can build a web-search client
// at run-time (Google creds live in site_configs).
type ResearchStore interface {
	SaveResearchResults(ctx context.Context, chatID, goal, report string, findings any) error
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// ResearchExecutionPayload is the JSON payload for a research-execution task.
type ResearchExecutionPayload struct {
	ResearchID string   `json:"researchId"`
	KbID       string   `json:"kbId"`
	Goal       string   `json:"goal"`
	MaxSteps   int      `json:"maxSteps"`
	Language   string   `json:"language"`
	Mode       string   `json:"mode"` // "kb" or "web"
	FileIDs    []string `json:"fileIds"`
	SessionID  string   `json:"sessionId"`
}

// NewResearchExecutionHandler returns an asynq.HandlerFunc that runs the
// research agent and publishes progress events to the Redis channel so the
// SSE relay can forward them to the waiting client.
func NewResearchExecutionHandler(
	resolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	rdb *redis.Client,
	store ResearchStore,
	sharedFetcher *fetcher.Fetcher,
) asynq.HandlerFunc {
	// Build the web-search client once per handler. nil-tolerant: if the
	// fetcher isn't configured (e.g. local dev without Chromium) we pass
	// a nil WebClient and the agent falls back to KB search for web mode.
	var webClient *research.WebClient
	if sharedFetcher != nil {
		webClient = research.NewWebClient(store, sharedFetcher)
	}
	return func(ctx context.Context, task *asynq.Task) error {
		var payload ResearchExecutionPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return err
		}

		slog.Info("research worker: starting",
			"researchId", payload.ResearchID,
			"kbId", payload.KbID,
			"goal", payload.Goal,
			"maxSteps", payload.MaxSteps,
		)

		// Derive a cancellable context that aborts when the client disconnects.
		abortKey := "research:abort:" + payload.ResearchID
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		safego.GoCtx(ctx, func() {
			watchAbortKey(ctx, cancel, rdb, abortKey)
		})

		agent := research.NewAgent(
			payload.KbID,
			payload.Goal,
			research.Options{
				MaxSteps: payload.MaxSteps,
				Language: payload.Language,
				Mode:     payload.Mode,
				FileIDs:  payload.FileIDs,
				AbortKey: "research:abort:" + payload.ResearchID,
			},
			resolver,
			searchSvc,
			rdb,
			webClient,
		)

		channel := "research:" + payload.ResearchID
		events := agent.Run(ctx)

		var lastReport string
		var lastFindings []research.Finding

		for event := range events {
			eventJSON, err := json.Marshal(event)
			if err != nil {
				slog.Warn("research worker: marshal event failed", "error", err)
				continue
			}

			if err := rdb.Publish(ctx, channel, string(eventJSON)).Err(); err != nil {
				slog.Warn("research worker: publish failed", "channel", channel, "error", err)
			}

			if event.Report != "" {
				lastReport = event.Report
			}
			if len(event.Findings) > 0 {
				lastFindings = append(lastFindings, event.Findings...)
			}
		}

		// Save final results to DB.
		if lastReport != "" && payload.SessionID != "" {
			if err := store.SaveResearchResults(ctx, payload.SessionID, payload.Goal, lastReport, lastFindings); err != nil {
				slog.Warn("research worker: save results failed", "sessionId", payload.SessionID, "error", err)
			}
		}

		// Signal completion to the SSE relay.
		if err := rdb.Publish(ctx, channel, "__done__").Err(); err != nil {
			slog.Warn("research worker: publish __done__ failed", "channel", channel, "error", err)
		}

		slog.Info("research worker: done",
			"researchId", payload.ResearchID,
			"report_len", len(lastReport),
			"findings", len(lastFindings),
		)

		return nil
	}
}
