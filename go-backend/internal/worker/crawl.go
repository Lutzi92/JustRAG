package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/crawler"
	"github.com/justrag/go-backend/internal/fetcher"
)

// CrawlDeps holds dependencies for the crawl worker. The fetcher is the
// same shared instance the server uses; Redis is used to publish progress
// the HTTP status endpoint polls.
type CrawlDeps struct {
	Fetcher *fetcher.Fetcher
	Redis   *redis.Client
}

// crawlProgressTTL controls how long a finished crawl's page list sticks
// around in Redis for the client to fetch. 30 minutes matches the podcast
// progress TTL and gives slow clients plenty of time to poll.
const crawlProgressTTL = 30 * time.Minute

// NewCrawlHandler returns an Asynq handler that executes one crawl job.
// It replicates the BFS semantics of the old synchronous crawler handler
// (same-domain, visited set, MaxPages cap, graceful skip on per-page
// errors), writes progress snapshots to Redis after each page, and
// publishes a final "completed" snapshot with the full page list.
func NewCrawlHandler(deps CrawlDeps) func(ctx context.Context, task *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload crawler.CrawlJobPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("crawl: unmarshal payload: %w", err)
		}

		jobID := task.ResultWriter().TaskID()
		progressKey := crawler.CrawlProgressKey(jobID)

		slog.Info("crawl: started",
			"jobId", jobID,
			"kbId", payload.KbID,
			"seedUrl", payload.SeedURL,
			"maxPages", payload.MaxPages,
		)

		// Helper to write the current state + progress to Redis. userId is
		// included so the HTTP status endpoint can verify ownership.
		publish := func(state string, progress map[string]any, pages []crawler.CrawlPage, errMsg string) {
			data := map[string]any{
				"state":    state,
				"progress": progress,
				"userId":   payload.UserID,
			}
			if pages != nil {
				data["pages"] = pages
			}
			if errMsg != "" {
				data["error"] = errMsg
			}
			b, _ := json.Marshal(data)
			if deps.Redis != nil {
				// Detached from the task ctx: the terminal "failed"/"completed"
				// publishes run after runCrawl returned — on a cancelled task
				// ctx a plain Set would silently no-op and the HTTP status
				// endpoint would show the job as active forever (until TTL).
				wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if err := deps.Redis.Set(wctx, progressKey, string(b), crawlProgressTTL).Err(); err != nil {
					slog.Warn("crawl: progress publish failed", "jobId", jobID, "state", state, "error", err)
				}
			}
		}

		publish("active", map[string]any{
			"step":    "start",
			"current": 0,
			"total":   payload.MaxPages,
			"message": "Starting crawl...",
		}, nil, "")

		if deps.Fetcher == nil {
			publish("failed", nil, nil, "crawl worker: fetcher not configured")
			return fmt.Errorf("crawl: fetcher not configured")
		}

		seed, err := url.Parse(payload.SeedURL)
		if err != nil {
			publish("failed", nil, nil, "invalid seed url")
			return fmt.Errorf("crawl: parse seed: %w", err)
		}

		pages, runErr := runCrawl(ctx, deps.Fetcher, seed, payload.SeedURL, payload.MaxPages,
			func(current int, lastURL string) {
				publish("active", map[string]any{
					"step":    "fetching",
					"current": current,
					"total":   payload.MaxPages,
					"message": fmt.Sprintf("Fetched %d/%d — %s", current, payload.MaxPages, lastURL),
				}, nil, "")
			})

		if runErr != nil && len(pages) == 0 {
			publish("failed", nil, nil, runErr.Error())
			return fmt.Errorf("crawl: run: %w", runErr)
		}

		publish("completed", map[string]any{
			"step":    "done",
			"current": len(pages),
			"total":   payload.MaxPages,
			"message": fmt.Sprintf("Crawled %d pages", len(pages)),
		}, pages, "")

		slog.Info("crawl: completed",
			"jobId", jobID,
			"kbId", payload.KbID,
			"pages", len(pages),
		)
		return nil
	}
}

// runCrawl is the BFS loop. Extracted from the old synchronous
// crawler.Handler.crawl method with identical semantics: visited-set
// dedup (fragment-stripped), same-domain filter, per-page failures
// skipped rather than fatal, readability → plain-text fallback for
// Title and Content when trafilatura declines a page.
func runCrawl(
	ctx context.Context,
	f *fetcher.Fetcher,
	seed *url.URL,
	seedURL string,
	maxPages int,
	onPage func(current int, lastURL string),
) ([]crawler.CrawlPage, error) {
	visited := make(map[string]bool)
	queue := []string{seedURL}
	var pages []crawler.CrawlPage

	for len(queue) > 0 && len(pages) < maxPages {
		select {
		case <-ctx.Done():
			return pages, ctx.Err()
		default:
		}

		rawURL := queue[0]
		queue = queue[1:]

		normalised := normaliseURL(rawURL)
		if visited[normalised] {
			continue
		}
		visited[normalised] = true

		res, err := f.Fetch(ctx, rawURL, fetcher.Options{
			Mode: fetcher.ModeAuto,
			// SkipExtraction defaults to false → markdown extraction is enabled.
		})
		if err != nil {
			slog.Debug("crawl: page fetch failed", "url", rawURL, "err", err)
			continue
		}

		title := res.Title
		content := res.Markdown
		if strings.TrimSpace(content) == "" {
			fallbackTitle, fallbackText := plainTextFallback(res.HTML)
			content = fallbackText
			if title == "" {
				title = fallbackTitle
			}
		}
		pages = append(pages, crawler.CrawlPage{
			Title:   title,
			URL:     res.URL,
			Content: content,
		})
		if onPage != nil {
			onPage(len(pages), res.URL)
		}

		for _, link := range res.Links {
			parsed, err := url.Parse(link)
			if err != nil {
				continue
			}
			if !sameDomain(seed, parsed) {
				continue
			}
			abs := seed.ResolveReference(parsed).String()
			if !visited[normaliseURL(abs)] {
				queue = append(queue, abs)
			}
		}
	}

	return pages, nil
}

// plainTextFallback strips script/style/nav/header/footer and returns the
// <title> text plus the body text. Moved here from the old
// crawler/handler.go when the crawl was moved to the worker.
func plainTextFallback(html string) (title, body string) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", ""
	}
	title = strings.TrimSpace(doc.Find("title").First().Text())
	doc.Find("script, style, nav, footer, header, noscript").Remove()
	text := doc.Find("body").Text()
	body = strings.Join(strings.Fields(text), " ")
	return title, body
}

// sameDomain reports whether target has the same host as base. Relative
// URLs (no host on target) are considered same-domain by definition.
func sameDomain(base, target *url.URL) bool {
	if target.Host == "" {
		return true
	}
	return strings.EqualFold(base.Hostname(), target.Hostname())
}

// normaliseURL strips the fragment for dedup purposes.
func normaliseURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Fragment = ""
	return parsed.String()
}
