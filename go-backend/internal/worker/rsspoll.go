package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mmcdole/gofeed"

	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/widcert"
)

// rssFetchTimeout is the per-item hard cap for a full-text article fetch.
// It covers the entire ModeAuto tier-escalation sequence (tier-1 then tier-2
// each receive a fresh Options.Timeout internally), so passing it as both the
// context deadline and the per-tier Options.Timeout guarantees no single item
// can spend more than rssFetchTimeout in total.
const rssFetchTimeout = 25 * time.Second

// urlFetcher is the minimal slice of *fetcher.Fetcher the RSS poller needs.
// Declaring it here keeps the dependency stubbable in tests.
type urlFetcher interface {
	Fetch(ctx context.Context, rawURL string, opts fetcher.Options) (*fetcher.Result, error)
}

// widResolver is the minimal slice of *widcert.Client the poller needs.
type widResolver interface {
	Fetch(ctx context.Context, name string) (*widcert.Advisory, error)
}

// siteConfigReader reads the WID enrichment kill switch.
type siteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// RSSPollDeps holds the dependencies for the RSS poll handler.
type RSSPollDeps struct {
	RSSStore    rss.RSSStore
	FileStore   files.Store
	Storage     storage.Storage
	AsynqClient *asynq.Client
	Fetcher     urlFetcher
	WIDClient   widResolver      // optional; nil disables WID enrichment
	SiteConfig  siteConfigReader // optional; nil = kill switch defaults ON
}

// NewRSSPollHandler returns an asynq.HandlerFunc that polls an RSS feed,
// creates file records for new items, and enqueues them for processing.
func NewRSSPollHandler(deps RSSPollDeps) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.RSSPollPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal rss poll payload: %w", err)
		}

		feedID := payload.FeedID
		slog.Info("RSS poll started", "feedId", feedID)

		// 1. Look up the feed.
		feed, err := deps.RSSStore.GetRSSFeedByID(ctx, feedID)
		if err != nil {
			return fmt.Errorf("get RSS feed: %w", err)
		}
		if feed == nil {
			slog.Warn("RSS feed not found, skipping", "feedId", feedID)
			return nil
		}
		if feed.Status != "active" {
			slog.Debug("RSS feed is not active, skipping", "feedId", feedID, "status", feed.Status)
			return nil
		}

		// 2. Fetch and parse the feed.
		parser := gofeed.NewParser()
		parsedFeed, err := parser.ParseURLWithContext(feed.URL, ctx)
		if err != nil {
			recordErr := deps.RSSStore.UpdateRSSFeedPollFailure(ctx, feedID, err.Error())
			if recordErr != nil {
				slog.Error("failed to record poll failure", "feedId", feedID, "error", recordErr)
			}
			return fmt.Errorf("parse RSS feed %s: %w", feed.URL, err)
		}

		// 3. Get existing item hashes for dedup. We dedup by the stable
		// identifier hash (GUID/link), not the filename which includes the
		// title and can change if the feed author edits the article.
		existingNames, err := deps.RSSStore.ListFileNamesByRSSFeedID(ctx, feedID)
		if err != nil {
			return fmt.Errorf("list existing RSS files: %w", err)
		}
		existingHashes := make(map[string]bool, len(existingNames))
		for name := range existingNames {
			if h := extractHashFromFileName(name); h != "" {
				existingHashes[h] = true
			}
		}

		// 4. Process each item.
		newCount := 0
		for _, item := range parsedFeed.Items {
			itemHash := rssItemHash(item)
			if existingHashes[itemHash] {
				continue
			}

			fileName := rssItemFileName(item)

			// Build markdown content, fetching the linked page's full text when enabled.
			content := resolveRSSItemContent(ctx, deps.Fetcher, deps.WIDClient, deps.SiteConfig, feed, item)

			// Store to storage.
			storagePath := storage.GetStoragePath("rss", feed.KbID, fileName)
			if storeErr := deps.Storage.StoreFile(ctx, storagePath, []byte(content), "text/markdown"); storeErr != nil {
				slog.Error("failed to store RSS item", "feedId", feedID, "item", fileName, "error", storeErr)
				continue
			}

			// Create file record linked to the RSS feed.
			fileRecord, createErr := deps.FileStore.CreateFile(ctx, files.CreateFileData{
				KbID:        feed.KbID,
				Name:        fileName,
				Type:        "text/markdown",
				Size:        len(content),
				Origin:      "rss",
				StoragePath: storagePath,
				RSSFeedID:   feedID,
			})
			if createErr != nil {
				slog.Error("failed to create file record for RSS item", "feedId", feedID, "item", fileName, "error", createErr)
				_ = deps.Storage.DeleteFile(ctx, storagePath)
				continue
			}

			// Enqueue file-processing job.
			jobPayload, _ := json.Marshal(jobs.FileProcessingPayload{
				FileID:       fileRecord.ID,
				KbID:         feed.KbID,
				FilePath:     storagePath,
				OriginalName: fileName,
				MimeType:     "text/markdown",
			})
			if _, enqErr := deps.AsynqClient.Enqueue(
				asynq.NewTask(jobs.TypeFileProcessing, jobPayload),
				asynq.Queue(jobs.QueueQuick),
				asynq.MaxRetry(3),
				asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
			); enqErr != nil {
				// Roll back: remove the orphaned file record and storage blob
				// so the next poll can retry this item cleanly.
				slog.Error("failed to enqueue RSS item processing, rolling back", "fileId", fileRecord.ID, "error", enqErr)
				_ = deps.FileStore.DeleteFileRecord(ctx, fileRecord.ID)
				_ = deps.Storage.DeleteFile(ctx, storagePath)
				continue
			}

			newCount++
		}

		// 5. Update feed with success.
		totalItems := feed.ItemCount + newCount
		if updateErr := deps.RSSStore.UpdateRSSFeedPollSuccess(ctx, feedID, totalItems); updateErr != nil {
			slog.Error("failed to update feed poll result", "feedId", feedID, "error", updateErr)
		}

		slog.Info("RSS poll completed", "feedId", feedID, "newItems", newCount, "totalItems", totalItems)
		return nil
	}
}

// rssItemHash returns a stable hex hash derived from the item's GUID or link.
// This is the dedup key — it doesn't change when the article title is edited.
func rssItemHash(item *gofeed.Item) string {
	identifier := item.GUID
	if identifier == "" {
		identifier = item.Link
	}
	if identifier == "" {
		identifier = item.Title
	}
	hash := sha256.Sum256([]byte(identifier))
	return hex.EncodeToString(hash[:8])
}

// rssItemFileName derives a human-readable filename with the stable hash suffix.
// Format: "Safe_Title_<hash>.md"
func rssItemFileName(item *gofeed.Item) string {
	shortHash := rssItemHash(item)

	title := strings.TrimSpace(item.Title)
	if len(title) > 80 {
		title = title[:80]
	}
	safeTitle := files.SafeNameSegment(title)
	if safeTitle == "" {
		safeTitle = "rss_item"
	}

	return safeTitle + "_" + shortHash + ".md"
}

// extractHashFromFileName extracts the 16-char hex hash from a filename like
// "Some_Title_abcdef0123456789.md". Returns empty string if not found.
func extractHashFromFileName(name string) string {
	// Strip .md extension.
	base := strings.TrimSuffix(name, ".md")
	// The hash is the last 16 chars after the final underscore.
	idx := strings.LastIndex(base, "_")
	if idx < 0 || idx+1 >= len(base) {
		return ""
	}
	candidate := base[idx+1:]
	if len(candidate) == 16 {
		return candidate
	}
	return ""
}

// writeRSSItemHeader writes the title/source/date/author metadata block.
func writeRSSItemHeader(sb *strings.Builder, item *gofeed.Item) {
	sb.WriteString("# ")
	sb.WriteString(item.Title)
	sb.WriteString("\n\n")

	if item.Link != "" {
		sb.WriteString("Source: ")
		sb.WriteString(item.Link)
		sb.WriteString("\n")
	}

	if item.PublishedParsed != nil {
		sb.WriteString("Published: ")
		sb.WriteString(item.PublishedParsed.Format("2006-01-02"))
		sb.WriteString("\n")
	} else if item.Published != "" {
		sb.WriteString("Published: ")
		sb.WriteString(item.Published)
		sb.WriteString("\n")
	}

	if item.Author != nil && item.Author.Name != "" {
		sb.WriteString("Author: ")
		sb.WriteString(item.Author.Name)
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
}

// feedItemBody returns the feed's own item body, preferring full Content over
// the shorter Description summary.
func feedItemBody(item *gofeed.Item) string {
	if item.Content != "" {
		return item.Content
	}
	return item.Description
}

// buildRSSItemContent converts an RSS item into markdown using the feed's own
// content/description. This is the fallback when full-text fetch is off or fails.
func buildRSSItemContent(item *gofeed.Item) string {
	var sb strings.Builder
	writeRSSItemHeader(&sb, item)
	sb.WriteString(feedItemBody(item))
	sb.WriteString("\n")
	return sb.String()
}

// minFullTextChars is the floor below which a fetched extraction is treated as
// junk rather than real article text. A JS-rendered single-page app served over
// plain HTTP yields only its shell (a title and some boilerplate), which is
// non-empty but useless; without this floor it would be stored as "full text"
// and silently replace the feed's own — often better — summary.
const minFullTextChars = 200

// isUsableFullText reports whether fetched markdown is substantial enough to
// prefer over the feed's own content: it must clear the absolute floor (to
// reject SPA-shell boilerplate) and be at least as long as what the feed already
// provides (so we only drop the summary when the fetch genuinely adds text).
func isUsableFullText(fetched, feedText string) bool {
	f := strings.TrimSpace(fetched)
	return len(f) >= minFullTextChars && len(f) >= len(strings.TrimSpace(feedText))
}

// buildRSSItemContentWithBody emits the metadata header followed by the
// fetched full article body, dropping the feed's own summary.
func buildRSSItemContentWithBody(item *gofeed.Item, body string) string {
	var sb strings.Builder
	writeRSSItemHeader(&sb, item)
	sb.WriteString(body)
	sb.WriteString("\n")
	return sb.String()
}

// widEnrichmentEnabled reports the kill switch state; defaults ON (nil reader,
// nil value, or unparseable all mean enabled).
func widEnrichmentEnabled(ctx context.Context, sc siteConfigReader) bool {
	if sc == nil {
		return true
	}
	v, err := sc.GetSiteConfigValue(ctx, "rss_wid_enrichment_enabled")
	if err != nil || v == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "false", "0":
		return false
	default:
		return true
	}
}

// resolveRSSItemContent returns the document body for an item. For
// wid.cert-bund.de links it fetches the structured WID JSON; otherwise it uses
// the generic full-text fetch, then the feed's own content. All paths fail open.
func resolveRSSItemContent(ctx context.Context, f urlFetcher, wid widResolver, sc siteConfigReader, feed *rss.RSSFeedRow, item *gofeed.Item) string {
	if feed.FetchFullText && item.Link != "" {
		// WID structured enrichment.
		if wid != nil && isWIDLink(item.Link) && widEnrichmentEnabled(ctx, sc) {
			if name := widcert.ExtractName(item.Title, item.GUID, item.Link); name != "" {
				adv, err := wid.Fetch(ctx, name)
				switch {
				case err == nil && adv != nil:
					return widcert.FormatMarkdown(adv)
				case err != nil:
					slog.Warn("WID enrichment failed, falling back",
						"feedId", feed.ID, "advisory", name, "error", httputil.SanitizeError(err))
				default:
					slog.Warn("WID enrichment returned no advisory, falling back",
						"feedId", feed.ID, "advisory", name)
				}
			}
		}

		// Generic full-text fetch.
		if f != nil {
			fctx, cancel := context.WithTimeout(ctx, rssFetchTimeout)
			res, err := f.Fetch(fctx, item.Link, fetcher.Options{Mode: fetcher.ModeAuto, Timeout: rssFetchTimeout})
			cancel()
			if err == nil && res != nil && isUsableFullText(res.Markdown, feedItemBody(item)) {
				return buildRSSItemContentWithBody(item, res.Markdown)
			}
			fetchedChars := 0
			if res != nil {
				fetchedChars = len(strings.TrimSpace(res.Markdown))
			}
			slog.Warn("RSS full-text fetch unusable, using feed content",
				"feedId", feed.ID, "link", item.Link, "error", err, "fetchedChars", fetchedChars)
		}
	}
	return buildRSSItemContent(item)
}

// isWIDLink reports whether link's host is the WID advisory host.
func isWIDLink(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), widcert.Host)
}
