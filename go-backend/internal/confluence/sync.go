package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/vector"
)

// confluenceImportConcurrency controls how many pages are imported in
// parallel within a single sync task. Configurable via
// CONFLUENCE_IMPORT_CONCURRENCY. Default 8 — tuned to fan out OCR + HTTP I/O
// while staying polite to the Confluence API.
var confluenceImportConcurrency = func() int {
	if raw := os.Getenv("CONFLUENCE_IMPORT_CONCURRENCY"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return 8
}()

// ChunkDeleter abstracts deleting vector chunks for files across every
// existing chunk-table dimension.
type ChunkDeleter interface {
	DeleteChunksByFileIDsAllDims(ctx context.Context, fileIDs []string) error
}

// SyncDeps holds the dependencies needed by the confluence sync handler.
type SyncDeps struct {
	Store        ConfluenceStore
	JWTSecret    string
	AsynqClient  *asynq.Client
	Storage      storage.Storage
	ChunkService ChunkDeleter
}

// NewSyncHandler returns an asynq.HandlerFunc that processes confluence-sync jobs.
func NewSyncHandler(deps SyncDeps) func(ctx context.Context, task *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.ConfluenceSyncPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal confluence sync payload: %w", err)
		}

		slog.Info("confluence sync started", "sourceId", payload.SourceID)

		if err := syncConfluenceSource(ctx, deps, payload.SourceID); err != nil {
			slog.Error("confluence sync failed", "sourceId", payload.SourceID, "error", err)
			return err
		}

		slog.Info("confluence sync completed", "sourceId", payload.SourceID)
		return nil
	}
}

// syncConfluenceSource is the main sync entry point. It handles both initial
// imports (no existing files) and incremental syncs.
func syncConfluenceSource(ctx context.Context, deps SyncDeps, sourceID string) error {
	source, err := deps.Store.GetConfluenceSourceByID(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	if source == nil {
		slog.Warn("confluence source not found, skipping", "sourceId", sourceID)
		return nil
	}
	if source.Status == "paused" {
		slog.Debug("confluence source is paused, skipping", "sourceId", sourceID)
		return nil
	}

	// Create an authenticated client.
	client, err := createClientForSync(ctx, deps, source.ConnectionID)
	if err != nil {
		return recordSyncFailure(ctx, deps.Store, source, err)
	}

	baseURL := getBaseURL(ctx, deps.Store)

	// Fetch current pages from Confluence.
	currentPages, err := fetchPages(ctx, client, source)
	if err != nil {
		return recordSyncFailure(ctx, deps.Store, source, err)
	}

	slog.Info("confluence pages fetched",
		"sourceId", sourceID,
		"spaceKey", source.SpaceKey,
		"pageCount", len(currentPages),
		"hasRootPage", source.RootPageID != nil && *source.RootPageID != "",
	)

	// Build map of current page IDs to page metadata.
	currentPageMap := make(map[string]ConfluencePage, len(currentPages))
	for _, p := range currentPages {
		currentPageMap[p.ID] = p
	}

	// Get existing files for this source.
	existingFiles, err := deps.Store.GetFilesByConfluenceSourceID(ctx, sourceID)
	if err != nil {
		return recordSyncFailure(ctx, deps.Store, source, err)
	}

	// Group existing files by confluence page ID.
	existingByPageID := make(map[string][]ConfluenceFileRow)
	for _, f := range existingFiles {
		if f.ConfluencePageID == nil {
			continue
		}
		existingByPageID[*f.ConfluencePageID] = append(existingByPageID[*f.ConfluencePageID], f)
	}

	var hasChanges atomic.Bool

	// 1. Delete files for pages no longer in Confluence.
	var filesToDelete []ConfluenceFileRow
	for pageID, files := range existingByPageID {
		if _, exists := currentPageMap[pageID]; !exists {
			filesToDelete = append(filesToDelete, files...)
		}
	}
	if len(filesToDelete) > 0 {
		if err := deleteConfluenceFiles(ctx, deps, filesToDelete); err != nil {
			slog.Warn("failed to delete removed page files", "sourceId", sourceID, "error", err)
		}
		hasChanges.Store(true)
	}

	// Count pages that will actually be imported (new + updated). This gives
	// the progress bar a non-zero target from the very first file completion,
	// avoiding the race where fast file processing finishes before the sync
	// handler reaches its final reconciliation block. Attachments imported
	// alongside pages will be reconciled by the post-file wrapper, which
	// writes the authoritative count from the DB.
	pagesToImport := 0
	for pageID, pageMeta := range currentPageMap {
		existingPageFiles := existingByPageID[pageID]
		if len(existingPageFiles) == 0 || isPageUpdated(pageMeta, existingPageFiles) {
			pagesToImport++
		}
	}

	// Mark as syncing. pageCount=0 signals the import loop is still running.
	// sync_total is pre-seeded with the page estimate so the UI shows progress
	// immediately.
	syncingStatus := "syncing"
	zero := 0
	_, _ = deps.Store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
		Status:       &syncingStatus,
		PageCount:    &zero,
		SyncProgress: &zero,
		SyncTotal:    &pagesToImport,
	})

	// 2. Process new and updated pages in parallel. A bounded worker pool
	// fans out page imports (each of which does HTTP fetches + OCR) while
	// keeping Confluence API load under control. successCount and hasChanges
	// are protected by a mutex.
	type pageJob struct {
		pageID   string
		pageMeta ConfluencePage
		existing []ConfluenceFileRow
		kind     string // "new", "updated", "skip"
	}

	jobs := make([]pageJob, 0, len(currentPageMap))
	for pageID, pageMeta := range currentPageMap {
		existingPageFiles := existingByPageID[pageID]
		kind := "skip"
		if len(existingPageFiles) == 0 {
			kind = "new"
		} else if isPageUpdated(pageMeta, existingPageFiles) {
			kind = "updated"
		}
		jobs = append(jobs, pageJob{
			pageID:   pageID,
			pageMeta: pageMeta,
			existing: existingPageFiles,
			kind:     kind,
		})
	}

	var successCount atomic.Int64
	markChange := func() { hasChanges.Store(true) }
	bumpSuccess := func() { successCount.Add(1) }

	sem := make(chan struct{}, confluenceImportConcurrency)
	var importWg sync.WaitGroup

dispatch:
	for _, j := range jobs {
		// Acquire a slot first so the loop bails immediately on ctx
		// cancellation instead of blocking on a full semaphore until an
		// in-flight import (still doing HTTP/OCR I/O) frees a slot.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		importWg.Add(1)
		// safego.GoCtx provides panic recovery: importPage / importPageAttachments
		// fan through HTTP, OCR, file store, and DB, and an uncaught panic in
		// any of those would otherwise crash the Asynq worker process. Both
		// importWg.Done and the semaphore release are inner defers so they
		// run before panic propagation reaches safego's recover.
		//
		// `j` is already per-iteration scoped under Go 1.22+ loop semantics
		// (this module declares `go 1.26`), so no shadow variable is needed.
		safego.GoCtx(ctx, func() {
			defer importWg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}

			switch j.kind {
			case "new":
				if err := importPage(ctx, deps, source, client, j.pageMeta, baseURL); err != nil {
					slog.Warn("failed to import confluence page",
						"sourceId", sourceID, "pageId", j.pageID, "error", err)
					return
				}
				if source.IncludeAttachments {
					importPageAttachments(ctx, deps, source, client, j.pageID)
				}
				markChange()
				bumpSuccess()

			case "updated":
				if err := deleteConfluenceFiles(ctx, deps, j.existing); err != nil {
					slog.Warn("failed to delete old page files", "pageId", j.pageID, "error", err)
				}
				if err := importPage(ctx, deps, source, client, j.pageMeta, baseURL); err != nil {
					slog.Warn("failed to re-import updated page",
						"sourceId", sourceID, "pageId", j.pageID, "error", err)
					return
				}
				if source.IncludeAttachments {
					importPageAttachments(ctx, deps, source, client, j.pageID)
				}
				markChange()
				bumpSuccess()

			case "skip":
				bumpSuccess()
			}
		})
	}
	importWg.Wait()

	// Cancellation is not success: without this check we would fall
	// through to finalize on the dead ctx (every UpdateConfluenceSource
	// below fails silently), return nil, and Asynq would record the task
	// as succeeded — stranding the source in status='syncing' until the
	// next scheduled tick. Returning the ctx error lets Asynq retry.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("confluence sync cancelled after %d/%d pages: %w",
			successCount.Load(), len(jobs), err)
	}

	// 3. Finalize: set pageCount to signal import loop is done, reconcile
	// sync_total/sync_progress against actual DB file counts, and decide
	// whether to mark the source active now or leave it for the post-file
	// wrapper to finalize when the last file finishes processing.
	now := time.Now().UTC()
	activeStatus := "active"
	empty := ""
	zeroFailures := 0
	successTotal := int(successCount.Load())
	pageCount := successTotal

	fileTotal, fileDone, _ := deps.Store.GetConfluenceSourceFileProgress(ctx, sourceID)

	if fileTotal == 0 {
		// No files at all (e.g. empty space) — mark active immediately.
		_, _ = deps.Store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
			Status:              &activeStatus,
			PageCount:           &pageCount,
			LastSyncedAt:        &now,
			SyncProgress:        &zero,
			SyncTotal:           &zero,
			ErrorMessage:        &empty,
			ConsecutiveFailures: &zeroFailures,
		})
	} else if fileDone >= fileTotal {
		// All files already processed — the post-file wrapper may have run
		// its final update before pageCount was set (importFinished=false at
		// the time), so finalize here.
		_, _ = deps.Store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
			Status:              &activeStatus,
			PageCount:           &pageCount,
			LastSyncedAt:        &now,
			SyncProgress:        &fileTotal,
			SyncTotal:           &fileTotal,
			ErrorMessage:        &empty,
			ConsecutiveFailures: &zeroFailures,
		})
	} else {
		// Files still processing — leave status as 'syncing', write the
		// authoritative file counts, and set pageCount so the post-file
		// wrapper knows the import loop is done and will flip to active
		// when done >= total.
		_, _ = deps.Store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
			PageCount:           &pageCount,
			SyncProgress:        &fileDone,
			SyncTotal:           &fileTotal,
			ErrorMessage:        &empty,
			ConsecutiveFailures: &zeroFailures,
		})
	}

	slog.Info("confluence sync finished",
		"sourceId", sourceID,
		"pageCount", successTotal,
		"totalPages", len(currentPages),
		"hasChanges", hasChanges.Load(),
	)
	return nil
}

// createClientForSync creates an authenticated ConfluenceClient from a connection ID.
func createClientForSync(ctx context.Context, deps SyncDeps, connectionID string) (*ConfluenceClient, error) {
	conn, err := deps.Store.GetConfluenceConnectionByID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("confluence connection %s not found", connectionID)
	}
	if conn.Status != "active" {
		return nil, fmt.Errorf("confluence connection %s is %s", connectionID, conn.Status)
	}

	plaintext, err := DecryptToken(conn.Token, deps.JWTSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt token: %w", err)
	}

	baseURL, err := deps.Store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil || baseURL == nil || *baseURL == "" {
		return nil, fmt.Errorf("confluence_base_url not configured")
	}

	return NewConfluenceClient(*baseURL, plaintext), nil
}

// getBaseURL retrieves the Confluence base URL from site config (non-critical).
func getBaseURL(ctx context.Context, store ConfluenceStore) string {
	val, err := store.GetSiteConfigValue(ctx, "confluence_base_url")
	if err != nil || val == nil {
		return ""
	}
	return strings.TrimRight(*val, "/")
}

// fetchPages fetches the appropriate pages based on source configuration.
func fetchPages(ctx context.Context, client *ConfluenceClient, source *ConfluenceSourceRow) ([]ConfluencePage, error) {
	if source.RootPageID != nil && *source.RootPageID != "" {
		// Subtree: include root page + descendants.
		rootPage, err := client.GetPageContent(ctx, *source.RootPageID)
		if err != nil {
			return nil, fmt.Errorf("get root page: %w", err)
		}
		descendants, err := client.GetPageTree(ctx, *source.RootPageID)
		if err != nil {
			return nil, fmt.Errorf("get page tree: %w", err)
		}
		return append([]ConfluencePage{rootPage}, descendants...), nil
	}

	// Whole space.
	pages, err := client.GetAllSpacePages(ctx, source.SpaceKey)
	if err != nil {
		return nil, fmt.Errorf("get space pages: %w", err)
	}
	return pages, nil
}

// isPageUpdated checks if a Confluence page has been modified after the
// existing file was created.
func isPageUpdated(page ConfluencePage, files []ConfluenceFileRow) bool {
	if page.Version.When == "" {
		return false
	}
	pageModified, err := time.Parse(time.RFC3339, page.Version.When)
	if err != nil {
		// Try alternate format (Confluence sometimes uses different formats).
		pageModified, err = time.Parse("2006-01-02T15:04:05.000Z", page.Version.When)
		if err != nil {
			return false
		}
	}

	// Find the markdown file (the main page content).
	for _, f := range files {
		if f.Type == "text/markdown" {
			return pageModified.After(f.CreatedAt)
		}
	}

	// No markdown file found — treat as new/updated.
	return true
}

// importPage fetches a page's content, converts to markdown, stores the file,
// and enqueues it for processing.
func importPage(
	ctx context.Context,
	deps SyncDeps,
	source *ConfluenceSourceRow,
	client *ConfluenceClient,
	pageMeta ConfluencePage,
	baseURL string,
) error {
	// Fetch full page content if body wasn't already loaded.
	page := pageMeta
	if page.BodyHTML == "" {
		var err error
		page, err = client.GetPageContent(ctx, pageMeta.ID)
		if err != nil {
			return fmt.Errorf("get page content: %w", err)
		}
	}

	html := page.BodyHTML
	if strings.TrimSpace(html) == "" {
		slog.Debug("skipping empty confluence page", "pageId", page.ID, "title", page.Title)
		return nil
	}

	// Extract text from inline images via OCR before stripping Confluence tags.
	html = processInlineImages(ctx, html, client, pageMeta.ID)

	// Handle draw.io diagram macros: replace each <ac:structured-macro
	// ac:name="drawio"> with a "[Diagram: name]" placeholder + extracted
	// cell labels so the diagram's content survives the tag stripper.
	html = processDrawioMacros(ctx, html, client, pageMeta.ID)

	// Resolve Confluence user mentions to "@Name" text. Runs before the
	// ac/ri stripper so the mention markup is still present to detect.
	// The returned list of resolved names is discarded here — emitting a
	// flat "mentioned persons" section was tried and removed: it conflated
	// editors / supervisors (who only touched the page's change history)
	// with actual project team members. The entity-mention index (see
	// kb_entity_mentions migration) handles person-lookup queries with
	// role awareness instead.
	html, _ = resolveUserMentions(ctx, html, client)

	// Build page URL.
	var pageURL string
	if baseURL != "" && page.WebUILink != "" {
		pageURL = baseURL + page.WebUILink
	}

	// Convert HTML to markdown.
	content := confluenceHTMLToMarkdown(html, page.Title, pageURL)
	fileName := safeFileName(page.Title) + ".md"
	storagePath := path.Join("confluence", source.KbID, source.ID, pageMeta.ID+"-"+safeFileName(page.Title)+".md")

	// Store the file.
	if err := deps.Storage.StoreFile(ctx, storagePath, []byte(content), "text/markdown"); err != nil {
		return fmt.Errorf("store file: %w", err)
	}

	// Create DB record.
	file, err := deps.Store.CreateConfluenceFile(ctx, CreateConfluenceFileData{
		KbID:               source.KbID,
		Name:               fileName,
		Type:               "text/markdown",
		Size:               len(content),
		Origin:             "confluence",
		StoragePath:        storagePath,
		ConfluenceSourceID: source.ID,
		ConfluencePageID:   pageMeta.ID,
	})
	if err != nil {
		return fmt.Errorf("create file record: %w", err)
	}

	// Enqueue for processing (chunking + embedding).
	payload, _ := json.Marshal(jobs.FileProcessingPayload{
		FileID:       file.ID,
		KbID:         source.KbID,
		FilePath:     storagePath,
		OriginalName: fileName,
		MimeType:     "text/markdown",
	})
	if _, err := deps.AsynqClient.Enqueue(
		asynq.NewTask(jobs.TypeFileProcessing, payload),
		asynq.Queue(jobs.QueueQuick),
		asynq.MaxRetry(3),
	); err != nil {
		slog.Warn("failed to enqueue confluence file processing",
			"fileId", file.ID, "error", err)
	}

	return nil
}

// importPageAttachments downloads and enqueues attachments for a page.
func importPageAttachments(
	ctx context.Context,
	deps SyncDeps,
	source *ConfluenceSourceRow,
	client *ConfluenceClient,
	pageID string,
) {
	attachments, err := client.GetPageAttachments(ctx, pageID)
	if err != nil {
		slog.Warn("failed to fetch page attachments", "pageId", pageID, "error", err)
		return
	}

	for _, att := range attachments {
		data, err := client.DownloadAttachment(ctx, att.DownloadLink)
		if err != nil {
			slog.Warn("failed to download attachment",
				"pageId", pageID, "title", att.Title, "error", err)
			continue
		}

		storagePath := path.Join("confluence", source.KbID, source.ID, "attachments", pageID+"-"+att.Title)

		if err := deps.Storage.StoreFile(ctx, storagePath, data, att.MediaType); err != nil {
			slog.Warn("failed to store attachment",
				"pageId", pageID, "title", att.Title, "error", err)
			continue
		}

		file, err := deps.Store.CreateConfluenceFile(ctx, CreateConfluenceFileData{
			KbID:               source.KbID,
			Name:               att.Title,
			Type:               att.MediaType,
			Size:               att.FileSize,
			Origin:             "confluence",
			StoragePath:        storagePath,
			ConfluenceSourceID: source.ID,
			ConfluencePageID:   pageID,
		})
		if err != nil {
			slog.Warn("failed to create attachment file record",
				"pageId", pageID, "title", att.Title, "error", err)
			continue
		}

		payload, _ := json.Marshal(jobs.FileProcessingPayload{
			FileID:       file.ID,
			KbID:         source.KbID,
			FilePath:     storagePath,
			OriginalName: att.Title,
			MimeType:     att.MediaType,
		})
		if _, err := deps.AsynqClient.Enqueue(
			asynq.NewTask(jobs.TypeFileProcessing, payload),
			asynq.Queue(jobs.QueueQuick),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
		); err != nil {
			slog.Warn("failed to enqueue attachment processing",
				"fileId", file.ID, "error", err)
		}
	}
}

// deleteConfluenceFiles removes vector chunks, storage files, and DB records
// for the given file rows.
func deleteConfluenceFiles(ctx context.Context, deps SyncDeps, files []ConfluenceFileRow) error {
	if len(files) == 0 {
		return nil
	}

	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}

	// Delete vector chunks across every existing chunk table (best effort).
	_ = deps.ChunkService.DeleteChunksByFileIDsAllDims(ctx, ids)

	// Delete storage files in one batched call (S3 DeleteObjects under the hood).
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if f.StoragePath != nil && *f.StoragePath != "" {
			paths = append(paths, *f.StoragePath)
		}
	}
	if len(paths) > 0 {
		_ = deps.Storage.DeleteFiles(ctx, paths)
	}

	// Delete DB records.
	return deps.Store.DeleteFilesByIDs(ctx, ids)
}

// recordSyncFailure updates the source with error status and increments the
// consecutive failure counter. Auto-pauses after 5 failures.
func recordSyncFailure(ctx context.Context, store ConfluenceStore, source *ConfluenceSourceRow, syncErr error) error {
	failures := source.ConsecutiveFailures + 1
	status := "error"
	if failures >= 5 {
		status = "paused"
	}
	errMsg := syncErr.Error()
	zero := 0
	now := time.Now().UTC()

	_, _ = store.UpdateConfluenceSource(ctx, source.ID, ConfluenceSourceUpdate{
		Status:              &status,
		ErrorMessage:        &errMsg,
		ConsecutiveFailures: &failures,
		PageCount:           &zero,
		SyncProgress:        &zero,
		SyncTotal:           &zero,
		LastSyncedAt:        &now,
	})

	slog.Error("confluence sync failed",
		"sourceId", source.ID,
		"error", syncErr,
		"failures", failures,
		"newStatus", status,
	)
	return syncErr
}

// ---------------------------------------------------------------------------
// HTML to Markdown conversion
// ---------------------------------------------------------------------------

// Collapse 3+ consecutive newlines to 2.
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

// acTagRe matches Confluence-specific XML macro tags (<ac:*>, <ri:*>).
var acTagRe = regexp.MustCompile(`</?(?:ac|ri):[^>]*>`)

// tagRe matches any HTML/XML tag — used as fallback when html-to-markdown fails.
var tagRe = regexp.MustCompile(`<[^>]+>`)

// unsafeFileNameRe matches characters not safe for filenames.
var unsafeFileNameRe = regexp.MustCompile(`[/\\<>:"|?*\x00-\x1f]`)

// sanitizeUTF8 replaces invalid UTF-8 byte sequences with spaces. Prevents
// PostgreSQL from rejecting content containing raw Latin-1 bytes (e.g. 0xa0).
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(' ')
			i++
		} else {
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// confluenceHTMLToMarkdown converts Confluence storage-format HTML to proper
// Markdown, preserving headings, lists, tables, links, code blocks, and
// emphasis. Uses html-to-markdown (Go equivalent of Turndown).
func confluenceHTMLToMarkdown(html, pageTitle, pageURL string) string {
	// Sanitize invalid UTF-8 sequences (e.g. raw 0xa0 non-breaking spaces).
	html = sanitizeUTF8(html)

	// Strip Confluence-specific macro tags that confuse the converter.
	html = acTagRe.ReplaceAllString(html, "")

	// Convert HTML to markdown using html-to-markdown (Turndown equivalent).
	markdown, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		// Fallback: strip all tags if conversion fails.
		markdown = tagRe.ReplaceAllString(html, "")
	}

	// Clean up excessive newlines.
	markdown = multiNewlineRe.ReplaceAllString(markdown, "\n\n")
	markdown = strings.TrimSpace(markdown)

	// Build header.
	var header strings.Builder
	header.WriteString("# " + sanitizeUTF8(pageTitle) + "\n\n")
	if pageURL != "" {
		header.WriteString("Source: " + pageURL + "\n\n")
	}

	return header.String() + markdown
}

// safeFileName sanitizes a title for use as a filename.
func safeFileName(title string) string {
	// Remove characters not safe for filenames.
	safe := unsafeFileNameRe.ReplaceAllString(title, "")
	safe = strings.TrimSpace(safe)
	if len(safe) > 100 {
		safe = safe[:100]
	}
	if safe == "" {
		safe = "untitled"
	}
	return safe
}

// ---------------------------------------------------------------------------
// Post-file-processing confluence progress update
// ---------------------------------------------------------------------------

// UpdateSourceProgressAfterFile checks if a processed file belongs to a
// confluence source and, if so, updates syncProgress. When all files are done
// and the import loop has finished (pageCount > 0), sets status to 'active'.
// Called from the file-processing worker wrapper — errors are non-fatal.
func UpdateSourceProgressAfterFile(ctx context.Context, store ConfluenceStore, taskPayload []byte) {
	var payload struct {
		FileID string `json:"fileId"`
	}
	if err := json.Unmarshal(taskPayload, &payload); err != nil || payload.FileID == "" {
		return
	}

	// Check if this file belongs to a confluence source.
	sourceID, err := store.GetConfluenceSourceIDForFile(ctx, payload.FileID)
	if err != nil || sourceID == "" {
		return // not a confluence file
	}

	source, err := store.GetConfluenceSourceByID(ctx, sourceID)
	if err != nil || source == nil || source.Status != "syncing" {
		return
	}

	total, done, err := store.GetConfluenceSourceFileProgress(ctx, sourceID)
	if err != nil || total == 0 {
		return
	}

	// pageCount > 0 means the sync handler has finished the import loop.
	importFinished := source.PageCount > 0

	if done >= total && importFinished {
		// All files processed and import loop is done — mark source active.
		// Keep sync_total/sync_progress populated (not zeroed) so the UI can
		// show a fully-filled bar briefly before polling refreshes it.
		now := time.Now().UTC()
		activeStatus := "active"
		empty := ""
		zeroFailures := 0
		_, _ = store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
			Status:              &activeStatus,
			LastSyncedAt:        &now,
			SyncProgress:        &total,
			SyncTotal:           &total,
			ErrorMessage:        &empty,
			ConsecutiveFailures: &zeroFailures,
		})
		slog.Info("all confluence files processed — source active",
			"sourceId", sourceID, "filesProcessed", done)
	} else {
		// Only update sync_progress — sync_total stays pinned to the initial
		// estimate the sync handler set at the start of the run. Writing
		// total=DB-count here would make total track done (because the import
		// loop creates files slower than workers process them), so the UI
		// would always see progress == total. The final file count is
		// reconciled by the sync handler's finalize block.
		_, _ = store.UpdateConfluenceSource(ctx, sourceID, ConfluenceSourceUpdate{
			SyncProgress: &done,
		})
	}
}

// Ensure ChunkDeleter is satisfied by vector.ChunkService.
var _ ChunkDeleter = (*vector.ChunkService)(nil)
