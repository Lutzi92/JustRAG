// Package files — ingest.go provides the AddTextSource and FetchURL handlers.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Input shapes
// ---------------------------------------------------------------------------

// addTextSourceRequest is the JSON body for POST /api/kb/{id}/text.
type addTextSourceRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// addSourcePage is a single page in an addSourcesRequest.
type addSourcePage struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// addSourcesRequest is the JSON body for POST /api/kb/{id}/add-sources.
type addSourcesRequest struct {
	Pages []addSourcePage `json:"pages"`
}

// fetchURLRequest is the JSON body for POST /api/kb/{id}/fetch-url.
// PreviewOnly returns the extracted content without storing a file or
// enqueuing processing — used by the websearch "edit before adding" flow.
type fetchURLRequest struct {
	URL         string `json:"url"`
	PreviewOnly bool   `json:"previewOnly"`
	Title       string `json:"title,omitempty"`
}

// fetchURLPreviewResponse is returned when PreviewOnly is true.
type fetchURLPreviewResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

// ---------------------------------------------------------------------------
// SSRF helpers — see fetcher.ValidateURL / fetcher.SafeHTTPClient.
// ---------------------------------------------------------------------------

// validateURL is a thin wrapper that pre-checks URL length, then delegates
// the scheme + private-IP check to fetcher.ValidateURL. The actual fetch
// re-resolves at dial time via fetcher.SafeHTTPClient, closing the DNS-
// rebinding window.
func validateURL(ctx context.Context, rawURL string) error {
	if len(rawURL) > 2048 {
		return fmt.Errorf("URL exceeds maximum length of 2048 characters")
	}
	return fetcher.ValidateURL(ctx, rawURL)
}

// ---------------------------------------------------------------------------
// HTTP fetch helper
// ---------------------------------------------------------------------------

// fetchPreview fetches rawURL with a plain HTTP client and extracts
// readable content. This is the lightweight path for previewOnly requests
// — no SSRF-safe dialer, no browser fallback, just a simple GET with
// readability extraction. The URL is assumed to be already validated by
// the caller.
func fetchPreview(ctx context.Context, rawURL, fallbackTitle string) (*fetchedContent, error) {
	client := fetcher.SafeHTTPClient(30 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("preview: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,de;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("preview: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("preview: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB cap for preview
	if err != nil {
		return nil, fmt.Errorf("preview: read body: %w", err)
	}

	html := string(body)
	title, md, extractErr := fetcher.Extract(html, rawURL)
	if extractErr == nil && strings.TrimSpace(md) != "" {
		if title == "" {
			title = strings.TrimSpace(fallbackTitle)
		}
		if title == "" {
			title = filenameFromURL(rawURL)
		}
		return &fetchedContent{
			Title:    title,
			Body:     []byte(md),
			MimeType: "text/markdown",
			Ext:      ".md",
		}, nil
	}

	// Extraction failed or returned empty — return raw HTML.
	title = strings.TrimSpace(fallbackTitle)
	if title == "" {
		title = filenameFromURL(rawURL)
	}
	return &fetchedContent{
		Title:    title,
		Body:     body,
		MimeType: "text/html",
		Ext:      ".html",
	}, nil
}

// fetchURLResponse holds the streaming response from fetchURL.
type fetchURLResponse struct {
	Body        io.ReadCloser
	ContentType string
}

// fetchURL fetches rawURL with a 30-second timeout and returns a streaming
// reader and Content-Type. The caller must close Body when done.
// The body is limited to 100 MB to prevent OOM on large downloads.
func fetchURL(ctx context.Context, rawURL string) (*fetchURLResponse, error) {
	client := fetcher.SafeHTTPClient(30 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch URL: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("remote server returned status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Strip parameters (e.g. "text/html; charset=utf-8" → "text/html").
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType != "" {
		contentType = mediaType
	}

	// Wrap body with a 100 MB limit.
	limited := io.LimitReader(resp.Body, 100<<20)
	body := struct {
		io.Reader
		io.Closer
	}{limited, resp.Body}

	return &fetchURLResponse{Body: body, ContentType: contentType}, nil
}

// ---------------------------------------------------------------------------
// Filename helpers
// ---------------------------------------------------------------------------

// sanitizeTitle converts a user-supplied title into a safe storage filename.
func sanitizeTitle(title string) string {
	safe := SafeNameSegment(strings.TrimSpace(title))
	if safe == "" {
		safe = "text_source"
	}
	return safe + ".txt"
}

// filenameFromURL derives a filename from the last path segment of rawURL.
// Falls back to the hostname if the path has no meaningful segment.
func filenameFromURL(rawURL string) string {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return "fetched_page"
	}

	base := path.Base(u.Path)
	if base == "" || base == "/" || base == "." {
		// Fall back to hostname.
		base = strings.ReplaceAll(u.Hostname(), ".", "_")
		if base == "" {
			base = "fetched_page"
		}
	}

	safe := SafeNameSegment(base)
	if safe == "" {
		safe = "fetched_page"
	}
	return safe
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// AddTextSource handles POST /api/kb/{id}/text.
// Requires authentication and KB edit permission (enforced by kbaccess middleware).
// Accepts {"title": "...", "content": "..."} and stores the content as a text file.
func (h *Handler) AddTextSource(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	// 1. Parse JSON body.
	var req addTextSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "title is required")
		return
	}
	if len(req.Title) > maxFileNameBytes {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("title must not exceed %d bytes", maxFileNameBytes))
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "content is required")
		return
	}
	if len(req.Content) > 500_000 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "content must not exceed 500,000 bytes")
		return
	}

	// 2. Resolve KB ID from path or kbaccess context.
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	// 3. Build storage path.
	username := user.Username
	safeFilename := sanitizeTitle(req.Title)
	storagePath := storage.GetStoragePath(username, kbID, safeFilename)

	// 4. Store content to storage.
	content := []byte(req.Content)
	if err := h.storage.StoreFile(r.Context(), storagePath, content, "text/plain"); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to store text content")
		return
	}

	// 5. Create file record (status: pending, origin: text).
	fileRecord, err := h.store.CreateFile(r.Context(), CreateFileData{
		KbID:        kbID,
		Name:        req.Title,
		Type:        "text/plain",
		Size:        len(content),
		Origin:      "text",
		StoragePath: storagePath,
	})
	if err != nil {
		_ = h.storage.DeleteFile(r.Context(), storagePath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to create file record")
		return
	}

	// 6. Enqueue file-processing job (reads from storage path).
	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(jobs.FileProcessingPayload{
			FileID:       fileRecord.ID,
			KbID:         kbID,
			FilePath:     storagePath,
			OriginalName: req.Title,
			MimeType:     "text/plain",
		})
		if marshalErr != nil {
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare processing job")
			return
		}
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeFileProcessing, payload),
			asynq.Queue(jobs.QueueQuick),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
		); enqErr != nil {
			logctx.From(r.Context()).Error("failed to enqueue text processing job", "error", enqErr)
			// Compensating cleanup: the file row + blob would otherwise be an
			// unprocessable orphan (no job will ever run). Delete so a client
			// retry is clean.
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue for processing")
			return
		}
	}

	// 7. Return 201.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, fileRecord)
}

// fetchedContent is the normalized output of the URL-fetching step,
// produced by either the shared Fetcher (preferred) or the legacy
// streaming fallback. Keeping both paths behind this shape lets the
// rest of FetchURL treat preview and storage uniformly.
type fetchedContent struct {
	Title    string
	Body     []byte // raw bytes to store / return
	MimeType string // e.g. "text/markdown", "text/html"
	Ext      string // filename extension including leading dot, e.g. ".md"
}

// fetchContentForIngest resolves req.URL into a fetchedContent. When a
// shared Fetcher is configured it runs Tier 1 → Tier 2 browser fallback
// with readability extraction and returns markdown. If extraction yields
// no markdown it falls back to raw HTML and records the MIME/extension
// as HTML so downstream storage and parsing stay honest. When no shared
// Fetcher is injected (tests, lightweight wiring) it uses the basic
// streaming fetch helper and reports the server's Content-Type.
func (h *Handler) fetchContentForIngest(ctx context.Context, req fetchURLRequest) (*fetchedContent, error) {
	if h.fetcher != nil {
		res, err := h.fetcher.Fetch(ctx, req.URL, fetcher.Options{Mode: fetcher.ModeAuto})
		if err != nil {
			return nil, err
		}
		// One-line audit of what the fetcher returned so operators can
		// tell "short preview" (thin markdown from a blocked page) apart
		// from "fetch failed" (upstream error) in the logs.
		logctx.From(ctx).Info("fetch-url: fetcher result",
			"url", req.URL,
			"tier", res.Tier,
			"status", res.StatusCode,
			"html_len", len(res.HTML),
			"markdown_len", len(res.Markdown),
			"title", res.Title)
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = strings.TrimSpace(req.Title)
		}
		if title == "" {
			title = filenameFromURL(req.URL)
		}
		if md := strings.TrimSpace(res.Markdown); md != "" {
			return &fetchedContent{
				Title:    title,
				Body:     []byte(res.Markdown),
				MimeType: "text/markdown",
				Ext:      ".md",
			}, nil
		}
		// Readability returned nothing — store the raw HTML as HTML so
		// the downstream parser/chunker doesn't embed tag soup as if it
		// were markdown.
		return &fetchedContent{
			Title:    title,
			Body:     []byte(res.HTML),
			MimeType: "text/html",
			Ext:      ".html",
		}, nil
	}

	// Legacy path: streaming HTTP fetch, no extraction. Buffer up to 10 MB
	// so preview works and we have a len() for CreateFileData.Size.
	resp, err := fetchURL(ctx, req.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = filenameFromURL(req.URL)
	}
	// Extension follows content type, not the URL — the legacy path
	// never did readability, so we just propagate whatever the server
	// sent (filenameFromURL is already used as a fallback name).
	ext := ""
	switch resp.ContentType {
	case "text/html":
		ext = ".html"
	case "text/markdown":
		ext = ".md"
	case "text/plain":
		ext = ".txt"
	}
	return &fetchedContent{
		Title:    title,
		Body:     body,
		MimeType: resp.ContentType,
		Ext:      ext,
	}, nil
}

// FetchURL handles POST /api/kb/{id}/fetch-url.
// Requires authentication and KB edit permission (enforced by kbaccess middleware).
// Accepts {"url": "https://...", "previewOnly": bool, "title": "..."}.
// When previewOnly is true it returns the extracted content as JSON
// without storing a file. Otherwise it stores the fetched content and
// enqueues a URL-processing job.
func (h *Handler) FetchURL(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	// 1. Parse JSON body.
	var req fetchURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "url is required")
		return
	}

	// 2. Validate URL (scheme + SSRF check).
	if err := validateURL(r.Context(), req.URL); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.SanitizeError(err))
		return
	}

	// 3. Resolve KB ID.
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	// 4. Preview — lightweight path. Uses a plain HTTP client (same as
	//    the websearch handler) instead of the SSRF-safe fetcher. The URL
	//    already passed scheme + private-IP validation above, and preview
	//    is read-only / non-persistent so the heavier fetcher machinery
	//    (custom dialer, browser escalation) is unnecessary and fragile
	//    in restricted server environments.
	if req.PreviewOnly {
		preview, err := fetchPreview(r.Context(), req.URL, req.Title)
		if err != nil {
			logctx.From(r.Context()).Error("fetch-url: preview failed", "url", req.URL, "kbID", kbID, "err", err)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadGateway, "Failed to fetch URL")
			return
		}
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, fetchURLPreviewResponse{
			Title:   preview.Title,
			Content: string(preview.Body),
			URL:     req.URL,
		})
		return
	}

	// 5. Full ingest — uses the shared Fetcher (SSRF-safe, browser fallback).
	fetched, err := h.fetchContentForIngest(r.Context(), req)
	if err != nil {
		logctx.From(r.Context()).Error("fetch-url: fetcher failed",
			"url", req.URL,
			"previewOnly", req.PreviewOnly,
			"kbID", kbID,
			"err", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadGateway, "Failed to fetch URL")
		return
	}

	// 5. Persist to storage with the correct MIME/extension.
	//    Titles are not unique (many sites return "Home", "Index", or the
	//    same article title), so we append a short URL-derived hash. Two
	//    different URLs can never collide on the same storage path even
	//    when their normalized titles match, while re-fetching the same
	//    URL is idempotent in storage (even though CreateFile still
	//    inserts a new row — the blob stays consistent).
	base := strings.TrimSuffix(sanitizeTitle(fetched.Title), ".txt")
	ext := fetched.Ext
	if ext == "" {
		ext = ".txt"
	}
	sum := sha256.Sum256([]byte(req.URL))
	suffix := hex.EncodeToString(sum[:4]) // 8 hex chars — enough to avoid practical collisions
	filename := fmt.Sprintf("%s_%s%s", base, suffix, ext)
	storagePath := storage.GetStoragePath(user.Username, kbID, filename)

	if err := h.storage.StoreFile(r.Context(), storagePath, fetched.Body, fetched.MimeType); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to store fetched content")
		return
	}

	fileRecord, err := h.store.CreateFile(r.Context(), CreateFileData{
		KbID:        kbID,
		Name:        filename,
		Type:        fetched.MimeType,
		Size:        len(fetched.Body),
		Origin:      "url",
		StoragePath: storagePath,
	})
	if err != nil {
		_ = h.storage.DeleteFile(r.Context(), storagePath)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Failed to create file record")
		return
	}

	// 6. Enqueue URL-processing job.
	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(jobs.URLProcessingPayload{
			FileID:       fileRecord.ID,
			KbID:         kbID,
			FilePath:     storagePath,
			OriginalName: filename,
			MimeType:     fetched.MimeType,
		})
		if marshalErr != nil {
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare processing job")
			return
		}
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeURLProcessing, payload),
			asynq.Queue(jobs.QueueHeavy),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeURLProcessing)),
		); enqErr != nil {
			logctx.From(r.Context()).Error("failed to enqueue URL processing job", "error", enqErr)
			// Compensating cleanup: the file row + blob would otherwise be an
			// unprocessable orphan (no job will ever run). Delete so a client
			// retry is clean.
			_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue for processing")
			return
		}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, fileRecord)
}

// addSourcesResponse is the JSON response for POST /api/kb/{id}/add-sources.
type addSourcesResponse struct {
	Message string        `json:"message"`
	Files   []*FileRecord `json:"files"`
}

// AddSources handles POST /api/kb/{id}/add-sources.
// Requires authentication and KB edit permission (enforced by kbaccess middleware).
// Accepts {"pages": [{"title": "...", "url": "...", "content": "..."}]} (max 20 pages)
// and stores each page as a markdown file, creating file records and enqueuing
// processing jobs.
func (h *Handler) AddSources(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "Authentication required")
		return
	}

	// 1. Parse JSON body.
	var req addSourcesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if len(req.Pages) == 0 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "pages must not be empty")
		return
	}
	if len(req.Pages) > 20 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "pages must not exceed 20 items")
		return
	}
	for i, page := range req.Pages {
		if len(page.Content) > 500_000 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("page %d content must not exceed 500,000 bytes", i+1))
			return
		}
	}

	// 2. Resolve KB ID from path or kbaccess context.
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(r.Context()); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	username := user.Username
	var created []*FileRecord

	// 3. For each page: build markdown content, store, create record, enqueue job.
	for i, page := range req.Pages {
		title := strings.TrimSpace(page.Title)
		if title == "" {
			title = "Untitled"
		}

		// Build markdown content.
		var sb strings.Builder
		sb.WriteString("# ")
		sb.WriteString(title)
		sb.WriteString("\n")
		if strings.TrimSpace(page.URL) != "" {
			sb.WriteString("Source: ")
			sb.WriteString(page.URL)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(page.Content)
		content := []byte(sb.String())

		// Derive safe filename with a URL-derived hash to avoid collisions
		// when multiple pages share the same title.
		safeBase := SafeNameSegment(title)
		if safeBase == "" {
			safeBase = "page"
		}
		urlForHash := strings.TrimSpace(page.URL)
		if urlForHash == "" {
			urlForHash = fmt.Sprintf("%s-%d", title, i)
		}
		sum := sha256.Sum256([]byte(urlForHash))
		suffix := hex.EncodeToString(sum[:4])
		filename := fmt.Sprintf("%s_%s.md", safeBase, suffix)
		storagePath := storage.GetStoragePath(username, kbID, filename)

		// Store file.
		if err := h.storage.StoreFile(r.Context(), storagePath, content, "text/markdown"); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, fmt.Sprintf("Failed to store page %q", title))
			return
		}

		// Create DB record.
		fileRecord, err := h.store.CreateFile(r.Context(), CreateFileData{
			KbID:        kbID,
			Name:        filename,
			Type:        "text/markdown",
			Size:        len(content),
			Origin:      "crawl",
			StoragePath: storagePath,
		})
		if err != nil {
			_ = h.storage.DeleteFile(r.Context(), storagePath)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, fmt.Sprintf("Failed to create file record for page %q", title))
			return
		}

		// Enqueue file-processing job.
		if h.asynqClient != nil {
			payload, marshalErr := json.Marshal(jobs.FileProcessingPayload{
				FileID:       fileRecord.ID,
				KbID:         kbID,
				FilePath:     storagePath,
				OriginalName: filename,
				MimeType:     "text/markdown",
			})
			if marshalErr != nil {
				_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
				_ = h.storage.DeleteFile(r.Context(), storagePath)
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to prepare processing job")
				return
			}
			if _, enqErr := h.asynqClient.Enqueue(
				asynq.NewTask(jobs.TypeFileProcessing, payload),
				asynq.Queue(jobs.QueueQuick),
				asynq.MaxRetry(3),
				asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
			); enqErr != nil {
				logctx.From(r.Context()).Error("failed to enqueue add-sources processing job", "fileId", fileRecord.ID, "error", enqErr)
				// Compensating cleanup: the file row + blob would otherwise be an
				// unprocessable orphan (no job will ever run). Delete so a client
				// retry is clean.
				_ = h.store.DeleteFileRecord(r.Context(), fileRecord.ID)
				_ = h.storage.DeleteFile(r.Context(), storagePath)
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to queue for processing")
				return
			}
		}

		created = append(created, fileRecord)
	}

	// 4. Return 201.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, addSourcesResponse{
		Message: fmt.Sprintf("%d sources added", len(created)),
		Files:   created,
	})
}
