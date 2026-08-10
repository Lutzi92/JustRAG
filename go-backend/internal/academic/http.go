package academic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/httpclient"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/sserelay"
	"github.com/justrag/go-backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// AcademicStore is the persistence interface required by Handler.
type AcademicStore interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
	CreateResearchSession(ctx context.Context, kbID, userID, goal, sessionType string) (*chat.ChatRow, error)
	CreateFile(ctx context.Context, data files.CreateFileData) (*files.FileRecord, error)
	GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error)
	GetKBShare(ctx context.Context, kbID, userID string) (*kbaccess.KBShare, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the academic endpoints.
type Handler struct {
	store       AcademicStore
	relay       *sserelay.Relay
	redis       *redis.Client
	stor        storage.Storage
	asynqClient *asynq.Client
	fetcher     *fetcher.Fetcher
}

// NewHandler creates an academic Handler.
func NewHandler(
	store AcademicStore,
	relay *sserelay.Relay,
	rdb *redis.Client,
	stor storage.Storage,
	asynqClient *asynq.Client,
	f *fetcher.Fetcher,
) *Handler {
	return &Handler{
		store:       store,
		relay:       relay,
		redis:       rdb,
		stor:        stor,
		asynqClient: asynqClient,
		fetcher:     f,
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// checkAcademicEnabled reads the site config and returns the base URL.
// Returns ("", false, err) on error, ("", false, nil) when disabled,
// (baseURL, true, nil) when enabled.
func (h *Handler) checkAcademicEnabled(ctx context.Context) (string, bool, error) {
	enabled, err := h.store.GetSiteConfigValue(ctx, "academic_search_enabled")
	if err != nil {
		return "", false, err
	}
	if enabled == nil || *enabled != "true" {
		return "", false, nil
	}
	baseURL, err := h.store.GetSiteConfigValue(ctx, "academic_search_base_url")
	if err != nil {
		return "", false, err
	}
	if baseURL == nil || *baseURL == "" {
		return "", false, nil
	}
	return *baseURL, true, nil
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/academic-search
// ---------------------------------------------------------------------------

type searchRequest struct {
	Query            string `json:"query"`
	Limit            int    `json:"limit"`
	PeerReviewedOnly bool   `json:"peerReviewedOnly"`
}

// Search handles POST /api/kb/{id}/academic-search.
// Performs a synchronous JustFind search and returns the list of papers as JSON.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	baseURL, ok, err := h.checkAcademicEnabled(r.Context())
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to read site config")
		return
	}
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "academic search is not enabled")
		return
	}

	var body searchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "query is required")
		return
	}

	// Guard against wiring that constructs the handler without a fetcher
	// (some tests, alternate server setups). SearchJustFind dereferences
	// the fetcher unconditionally, so a nil here would panic the request
	// instead of returning a clean HTTP error.
	if h.fetcher == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "academic search fetcher not configured")
		return
	}

	papers, err := SearchJustFind(r.Context(), h.fetcher, baseURL, body.Query, SearchOptions{
		PeerReviewedOnly: body.PeerReviewedOnly,
		Limit:            body.Limit,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadGateway, "academic search failed")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"papers": papers})
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/academic-research
// ---------------------------------------------------------------------------

type startResearchRequest struct {
	Topic            string          `json:"topic"`
	MaxSteps         int             `json:"maxSteps"`
	Language         string          `json:"language"`
	PeerReviewedOnly bool            `json:"peerReviewedOnly"`
	InitialPapers    []AcademicPaper `json:"initialPapers"`
}

// StartResearch handles POST /api/kb/{id}/academic-research.
// Creates a research session and starts an SSE-streamed worker job.
func (h *Handler) StartResearch(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	kbID := r.PathValue("id")

	_, ok, err := h.checkAcademicEnabled(r.Context())
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to read site config")
		return
	}
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "academic search is not enabled")
		return
	}

	var body startResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Topic) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "topic is required")
		return
	}
	if body.MaxSteps <= 0 {
		body.MaxSteps = 10
	}
	if body.Language == "" {
		body.Language = "en"
	}

	ctx := r.Context()

	session, err := h.store.CreateResearchSession(ctx, kbID, user.ID, body.Topic, "academic-research")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create research session")
		return
	}

	researchID := uuid.New().String()

	type workerPayload struct {
		ResearchID       string          `json:"researchId"`
		KbID             string          `json:"kbId"`
		Topic            string          `json:"topic"`
		MaxSteps         int             `json:"maxSteps"`
		Language         string          `json:"language"`
		PeerReviewedOnly bool            `json:"peerReviewedOnly"`
		InitialPapers    []AcademicPaper `json:"initialPapers"`
		SessionID        string          `json:"sessionId"`
	}

	payload, err := json.Marshal(workerPayload{
		ResearchID:       researchID,
		KbID:             kbID,
		Topic:            body.Topic,
		MaxSteps:         body.MaxSteps,
		Language:         body.Language,
		PeerReviewedOnly: body.PeerReviewedOnly,
		InitialPapers:    body.InitialPapers,
		SessionID:        session.ID,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to build job payload")
		return
	}

	channel := "academic-research:" + researchID
	abortKey := "academic-research:abort:" + researchID

	// Write SSE headers + session-id preamble event before handing off to relay.
	httputil.EnableSSE(w)

	sessionEvent, _ := json.Marshal(map[string]string{"type": "session", "sessionId": session.ID})
	_, _ = w.Write([]byte("data: " + string(sessionEvent) + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	if err := h.relay.Run(ctx, w, sserelay.Options{
		Channel:    channel,
		AbortKey:   abortKey,
		JobType:    jobs.TypeAcademicResearchExecution,
		JobPayload: payload,
	}); err != nil {
		// Headers already sent; nothing to do.
		return
	}
}

// ---------------------------------------------------------------------------
// POST /api/academic-research/{researchId}/papers/add
// ---------------------------------------------------------------------------

type addPapersRequest struct {
	KbID   string          `json:"kbId"`
	Papers []AcademicPaper `json:"papers"`
}

type addPapersResult struct {
	Added  []string `json:"added"`
	Failed []string `json:"failed"`
}

// AddPapers handles POST /api/academic-research/{researchId}/papers/add.
// For each paper with a pdfUrl it fetches the PDF, validates the magic bytes,
// stores the file, creates a DB record, and enqueues a file-processing job.
func (h *Handler) AddPapers(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body addPapersRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.KbID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "kbId is required")
		return
	}

	// Verify the caller has access to the target KB before writing files into it.
	kb, err := h.store.GetKBByID(r.Context(), body.KbID)
	if err != nil || kb == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "knowledge base not found")
		return
	}
	isOwner := kb.UserID != nil && *kb.UserID == user.ID
	isAdmin := user.Role == "admin" || user.Role == "superadmin"
	hasEditAccess := isOwner || isAdmin
	if !hasEditAccess {
		// Check for edit share
		share, shareErr := h.store.GetKBShare(r.Context(), body.KbID, user.ID)
		if shareErr == nil && share != nil && share.Permission == "edit" {
			hasEditAccess = true
		}
	}
	if !hasEditAccess {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "you do not have edit access to this knowledge base")
		return
	}

	result := addPapersResult{
		Added:  []string{},
		Failed: []string{},
	}

	// AddPapers intentionally uses a dedicated httpclient.New(30 * time.Second)
	// rather than the injected h.fetcher used by Search. h.fetcher is an
	// HTML-oriented tier1/browser pipeline that extracts and returns parsed
	// content (see fetcher.Fetcher.Fetch), which is the wrong shape for
	// downloading raw PDF bytes. A plain pooled http.Client gives us direct
	// access to the response body for magic-byte validation and the 200 MB
	// LimitReader cap below.
	httpClient := httpclient.New(30 * time.Second)

	for _, paper := range body.Papers {
		if paper.PdfURL == "" {
			result.Failed = append(result.Failed, paper.ID)
			continue
		}

		id := paper.ID
		if id == "" {
			id = stableID(paper.PdfURL)
		}

		if err := h.downloadAndStorePaper(r.Context(), httpClient, user.Username, body.KbID, paper); err != nil {
			result.Failed = append(result.Failed, id)
			continue
		}
		result.Added = append(result.Added, id)
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}

// validateURL checks that a URL is safe to fetch (SSRF protection).
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https allowed")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("DNS lookup failed: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("private/loopback IP not allowed")
		}
	}
	return nil
}

// downloadAndStorePaper fetches a PDF, validates it, stores it, creates a DB
// record, and enqueues a file-processing job.
func (h *Handler) downloadAndStorePaper(
	ctx context.Context,
	client *http.Client,
	username, kbID string,
	paper AcademicPaper,
) error {
	// If the URL points to a JustFind catalogue page rather than a direct
	// PDF, attempt to extract the real PDF download link via the browser.
	actualURL := paper.PdfURL
	if isJustFindURL(paper.PdfURL) && h.fetcher != nil {
		extracted, err := extractPDFFromJustFind(ctx, h.fetcher, paper.PdfURL)
		if err != nil {
			logctx.From(ctx).Warn("JustFind extraction failed, trying direct URL", "error", err)
		} else {
			actualURL = extracted
		}
	}

	// SSRF validation: only allow http/https, block private/loopback IPs.
	if err := validateURL(actualURL); err != nil {
		return fmt.Errorf("SSRF check: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actualURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "JustRAG/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch pdf: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch pdf: status %d", resp.StatusCode)
	}

	pdfBytes, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20)) // 200 MB cap
	if err != nil {
		return fmt.Errorf("read pdf body: %w", err)
	}

	// Validate PDF magic bytes (%PDF).
	if len(pdfBytes) < 4 || !bytes.HasPrefix(pdfBytes, []byte("%PDF")) {
		return fmt.Errorf("not a valid PDF (missing %%PDF magic bytes)")
	}

	// Build a safe filename from the paper title.
	filename := safeFilename(paper.Title) + ".pdf"
	storagePath := storage.GetStoragePath(username, kbID, filename)

	if err := h.stor.StoreFile(ctx, storagePath, pdfBytes, "application/pdf"); err != nil {
		return fmt.Errorf("store pdf: %w", err)
	}

	fileRecord, err := h.store.CreateFile(ctx, files.CreateFileData{
		KbID:        kbID,
		Name:        filename,
		Type:        "application/pdf",
		Size:        len(pdfBytes),
		Origin:      "research",
		StoragePath: storagePath,
	})
	if err != nil {
		_ = h.stor.DeleteFile(ctx, storagePath)
		return fmt.Errorf("create file record: %w", err)
	}

	if h.asynqClient != nil {
		payload, marshalErr := json.Marshal(jobs.FileProcessingPayload{
			FileID:       fileRecord.ID,
			KbID:         kbID,
			FilePath:     storagePath,
			OriginalName: filename,
			MimeType:     "application/pdf",
		})
		if marshalErr != nil {
			return fmt.Errorf("marshal file processing payload: %w", marshalErr)
		}
		if _, enqErr := h.asynqClient.Enqueue(
			asynq.NewTask(jobs.TypeFileProcessing, payload),
			asynq.Queue(jobs.QueueQuick),
			asynq.MaxRetry(3),
			asynq.Timeout(jobs.TimeoutFor(jobs.TypeFileProcessing)),
		); enqErr != nil {
			return fmt.Errorf("enqueue file processing: %w", enqErr)
		}
	}

	return nil
}

// safeFilename converts a paper title into a filesystem-safe filename (no extension).
func safeFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "paper"
	}
	var sb strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			sb.WriteRune('_')
		}
	}
	s := sb.String()
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		return "paper"
	}
	return s
}
