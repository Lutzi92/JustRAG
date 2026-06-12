package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/PuerkitoBio/goquery"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/safego"
)

// AcademicPaperRef is a minimal academic paper representation used in the
// worker payload. It mirrors academic.AcademicPaper to avoid a circular import
// (academic -> files -> worker -> academic).
type AcademicPaperRef struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Year     int      `json:"year"`
	Journal  string   `json:"journal,omitempty"`
	Abstract string   `json:"abstract,omitempty"`
	DOI      string   `json:"doi,omitempty"`
	URL      string   `json:"url"`
	PdfURL   string   `json:"pdfUrl,omitempty"`
}

// AcademicResearchStore is the persistence interface needed by the academic worker.
type AcademicResearchStore interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
	SaveResearchResults(ctx context.Context, chatID, goal, report string, findings any) error
}

// AcademicResearchPayload is the JSON payload for an academic-research-execution task.
type AcademicResearchPayload struct {
	ResearchID       string             `json:"researchId"`
	KbID             string             `json:"kbId"`
	Topic            string             `json:"topic"`
	MaxSteps         int                `json:"maxSteps"`
	Language         string             `json:"language"`
	PeerReviewedOnly bool               `json:"peerReviewedOnly"`
	InitialPapers    []AcademicPaperRef `json:"initialPapers"`
	SessionID        string             `json:"sessionId"`
}

// NewAcademicResearchHandler returns an asynq.HandlerFunc that executes an
// academic research job, publishing progress events to a Redis channel so the
// SSE relay can forward them to the waiting client.
func NewAcademicResearchHandler(
	resolver *ai.ConfigResolver,
	rdb *redis.Client,
	store AcademicResearchStore,
	f *fetcher.Fetcher,
) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload AcademicResearchPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return err
		}

		slog.Info("academic research worker: starting",
			"researchId", payload.ResearchID,
			"kbId", payload.KbID,
			"topic", payload.Topic,
			"maxSteps", payload.MaxSteps,
		)

		// Derive a cancellable context that aborts when the client disconnects.
		abortKey := "academic-research:abort:" + payload.ResearchID
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		safego.GoCtx(ctx, func() {
			watchAbortKey(ctx, cancel, rdb, abortKey)
		})

		channel := "academic-research:" + payload.ResearchID

		publish := func(event map[string]any) {
			data, err := json.Marshal(event)
			if err != nil {
				slog.Warn("academic research worker: marshal event failed", "error", err)
				return
			}
			if err := rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
				slog.Warn("academic research worker: publish failed", "channel", channel, "error", err)
			}
		}

		publish(map[string]any{
			"type":       "started",
			"message":    "Academic research started",
			"stepNumber": 0,
			"totalSteps": payload.MaxSteps,
		})

		// Use caller-provided papers when available; otherwise search JustFind.
		papers := payload.InitialPapers
		if len(papers) == 0 {
			baseURL, err := store.GetSiteConfigValue(ctx, "academic_search_base_url")
			if err == nil && baseURL != nil && *baseURL != "" {
				publish(map[string]any{
					"type":       "searching",
					"message":    "Searching academic database",
					"stepNumber": 1,
					"totalSteps": payload.MaxSteps,
				})

				found, searchErr := workerSearchJustFind(ctx, f, *baseURL, payload.Topic, payload.PeerReviewedOnly, 20)
				if searchErr != nil {
					slog.Warn("academic research worker: search failed", "error", searchErr)
				} else {
					papers = found
				}
			}
		}

		publish(map[string]any{
			"type":       "synthesizing",
			"message":    "Synthesising findings",
			"stepNumber": 2,
			"totalSteps": payload.MaxSteps,
		})

		lang := payload.Language
		if lang == "" {
			lang = "en"
		}

		paperSummaries := academicBuildPaperSummaries(papers)
		systemPrompt := academicBuildSystemPrompt(lang)
		userPrompt := academicBuildUserPrompt(payload.Topic, paperSummaries, lang)

		result, err := ai.GenerateCompletion(ctx, resolver, userPrompt, systemPrompt, payload.KbID, false)
		var report string
		if err != nil || result == nil {
			slog.Warn("academic research worker: generation failed", "error", err)
			report = "# Academic Research Report\n\n**Topic:** " + payload.Topic +
				"\n\nResearch synthesis could not be generated. Please review the papers manually."
		} else {
			report = result.Content
		}

		publish(map[string]any{
			"type":       "complete",
			"message":    "Academic research complete",
			"stepNumber": payload.MaxSteps,
			"totalSteps": payload.MaxSteps,
			"report":     report,
		})

		if payload.SessionID != "" {
			if err := store.SaveResearchResults(ctx, payload.SessionID, payload.Topic, report, papers); err != nil {
				slog.Warn("academic research worker: save results failed",
					"sessionId", payload.SessionID, "error", err)
			}
		}

		if err := rdb.Publish(ctx, channel, "__done__").Err(); err != nil {
			slog.Warn("academic research worker: publish __done__ failed", "channel", channel, "error", err)
		}

		slog.Info("academic research worker: done",
			"researchId", payload.ResearchID,
			"papers", len(papers),
		)
		return nil
	}
}

// ---------------------------------------------------------------------------
// Internal JustFind search (mirrors academic.SearchJustFind without the import)
// ---------------------------------------------------------------------------

func workerSearchJustFind(ctx context.Context, f *fetcher.Fetcher, baseURL, query string, peerReviewedOnly bool, limit int) ([]AcademicPaperRef, error) {
	searchURL := fmt.Sprintf(
		"%s/EDS/Search?lookfor=%s&type=AllFields&limit=%d&filter[]=LIMIT%%7CFT:%%22y%%22",
		strings.TrimRight(baseURL, "/"),
		url.QueryEscape(query),
		limit,
	)
	if peerReviewedOnly {
		searchURL += "&filter[]=LIMIT%7CRV:y"
	}

	res, err := f.Fetch(ctx, searchURL, fetcher.Options{
		Mode:           fetcher.ModeJustFind,
		SkipExtraction: true, // we scrape specific selectors; markdown extraction is not useful here
		// baseURL comes from trusted site config, not user input. Opt out
		// of SSRF private-IP rejection so intranet JustFind instances
		// remain reachable.
		AllowPrivateHosts: true,
	})
	// Fallback: retry with plain HTTP on ANY Tier 3 failure. Covers the
	// missing-chromium-build case (ErrUnsupportedMode) AND runtime failures
	// in chromium builds — Chromium binary missing on the host,
	// JUSTFIND_USER_DATA_DIR unwritable, rod launcher/connect flakes,
	// sandbox denied, etc. Plain HTTP is still a viable path for JustFind
	// instances without Anubis, so falling back preserves the pre-rebuild
	// behaviour for non-container `go run ./cmd/worker` workflows.
	// Invalid URLs (SSRF rejection) fail identically in Tier 1, so the
	// fallback is safe. Mirrors academic.SearchJustFind.
	if err != nil {
		slog.Warn("worker/academic: Tier 3 fetch failed, falling back to plain HTTP",
			"query", query, "err", err)
		res, err = f.Fetch(ctx, searchURL, fetcher.Options{
			Mode:              fetcher.ModeHTTPOnly,
			SkipExtraction:    true,
			AllowPrivateHosts: true,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("justfind: fetch: %w", err)
	}

	// Tier 1 exposes the real upstream HTTP status; reject non-200 here
	// so a templated 500/404 page that still ships shared VuFind layout
	// doesn't get parsed as an empty successful search.
	//
	// Scoped to Tier 1 only: Tier 3 (rod) does not reliably expose the
	// main-frame HTTP status (falls back to 0 on missed events, and
	// redirect/challenge flows can leave non-200 codes even when the
	// final HTML is valid). The structural-marker check below handles
	// Tier 3 instead.
	if res.Tier == 1 && res.StatusCode != 200 {
		return nil, fmt.Errorf("justfind: upstream HTTP %d", res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(res.HTML))
	if err != nil {
		return nil, err
	}

	// Same guard as academic.SearchJustFind: Tier 3 reports StatusCode=200
	// for every successful navigation, so an upstream 404/500/challenge
	// page would otherwise parse as an empty result set. Require a
	// recognisable VuFind/JustFind marker before trusting the response.
	hasMarker := false
	for _, sel := range []string{
		".result-list-item", ".no-results", ".no-records-found",
		"#search-home", ".search-home", ".mainbody",
	} {
		if doc.Find(sel).Length() > 0 {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		return nil, fmt.Errorf("justfind: unexpected page content (no VuFind markers — upstream error or challenge page)")
	}

	var papers []AcademicPaperRef
	doc.Find(".result-list-item").Each(func(_ int, s *goquery.Selection) {
		p := AcademicPaperRef{}

		if t := strings.TrimSpace(s.Find(".title a, h3 a, .result-title a").First().Text()); t != "" {
			p.Title = t
		}
		if href, exists := s.Find(".title a, h3 a, .result-title a").First().Attr("href"); exists {
			if strings.HasPrefix(href, "http") {
				p.URL = href
			} else {
				p.URL = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(href, "/")
			}
		}
		if coinsTitle, exists := s.Find("span.Z3988").Attr("title"); exists {
			workerParseCOinS(&p, coinsTitle)
		}
		if p.ID == "" {
			if p.URL != "" {
				p.ID = academicStableID(p.URL)
			} else {
				p.ID = academicStableID(p.Title)
			}
		}
		if p.Title != "" {
			papers = append(papers, p)
		}
	})

	return papers, nil
}

func workerParseCOinS(p *AcademicPaperRef, coinsTitle string) {
	decoded, err := url.QueryUnescape(coinsTitle)
	if err != nil {
		decoded = coinsTitle
	}
	vals := make(map[string][]string)
	for _, part := range strings.Split(decoded, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			k, v := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
			if k != "" && v != "" {
				vals[k] = append(vals[k], v)
			}
		}
	}
	workerFirstVal := func(keys ...string) string {
		for _, k := range keys {
			if vs := vals[k]; len(vs) > 0 {
				return vs[0]
			}
		}
		return ""
	}

	if v := workerFirstVal("rft.atitle"); v != "" && p.Title == "" {
		p.Title = v
	}
	if v := workerFirstVal("rft.jtitle", "rft.btitle"); v != "" {
		p.Journal = v
	}
	for _, a := range vals["rft.au"] {
		if a != "" {
			p.Authors = append(p.Authors, a)
		}
	}
	if len(p.Authors) == 0 {
		first := workerFirstVal("rft.aufirst")
		last := workerFirstVal("rft.aulast")
		if last != "" {
			if first != "" {
				p.Authors = append(p.Authors, first+" "+last)
			} else {
				p.Authors = append(p.Authors, last)
			}
		}
	}
	if p.Year == 0 {
		if v := workerFirstVal("rft.date"); v != "" && len(v) >= 4 {
			year := 0
			for _, c := range v[:4] {
				if c >= '0' && c <= '9' {
					year = year*10 + int(c-'0')
				}
			}
			p.Year = year
		}
	}
	if v := workerFirstVal("rft_id"); v != "" {
		if strings.HasPrefix(v, "info:doi/") {
			p.DOI = strings.TrimPrefix(v, "info:doi/")
			if p.ID == "" {
				p.ID = p.DOI
			}
		} else if p.ID == "" {
			p.ID = v
		}
	}
}

func academicStableID(s string) string {
	var h uint64 = 5381
	for i := 0; i < len(s); i++ {
		h = h*33 + uint64(s[i])
	}
	return fmt.Sprintf("%016x", h)
}

// ---------------------------------------------------------------------------
// Prompt builders
// ---------------------------------------------------------------------------

func academicBuildPaperSummaries(papers []AcademicPaperRef) string {
	if len(papers) == 0 {
		return "(no papers available)"
	}
	var sb strings.Builder
	for i, p := range papers {
		fmt.Fprintf(&sb, "%d. ", i+1)
		if p.Title != "" {
			sb.WriteString(p.Title)
		}
		if len(p.Authors) > 0 {
			sb.WriteString(" — " + p.Authors[0])
		}
		if p.Year > 0 {
			fmt.Fprintf(&sb, " (%d)", p.Year)
		}
		if p.Abstract != "" {
			ab := p.Abstract
			if len(ab) > 400 {
				ab = ab[:400] + "…"
			}
			sb.WriteString("\nAbstract: " + ab)
		}
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func academicBuildSystemPrompt(lang string) string {
	if lang == "de" {
		return "Du bist ein wissenschaftlicher Forschungsassistent. " +
			"Analysiere die bereitgestellten akademischen Arbeiten und erstelle einen fundierten Forschungsbericht auf Deutsch."
	}
	return "You are a scientific research assistant. " +
		"Analyse the provided academic papers and write a comprehensive research report in English."
}

func academicBuildUserPrompt(topic, paperSummaries, lang string) string {
	if lang == "de" {
		return "Forschungsthema: " + topic + "\n\nVerfügbare Arbeiten:\n" + paperSummaries +
			"\nErstelle einen strukturierten Forschungsbericht zu diesem Thema auf Basis der verfügbaren Arbeiten."
	}
	return "Research topic: " + topic + "\n\nAvailable papers:\n" + paperSummaries +
		"\nWrite a structured research report on this topic based on the available papers."
}
