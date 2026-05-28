// Package misc implements miscellaneous utility endpoints:
//   - POST /api/enhance        — query enhancement (rewrite / expand / spell)
//   - POST /api/research/{sessionId}/bibtex          — research BibTeX export
//   - POST /api/academic-research/{sessionId}/bibtex — academic BibTeX export
package misc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/research"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Finding represents a single research finding with associated sources.
type Finding struct {
	Sources []Source `json:"sources"`
}

// Source represents a document source cited in a research finding.
type Source struct {
	FileID   string `json:"fileId"`
	FileName string `json:"fileName"`
	URL      string `json:"url"`
}

// AcademicPaper represents an academic paper with bibliographic metadata.
type AcademicPaper struct {
	ID      string   `json:"id"`
	Authors []string `json:"authors"`
	Title   string   `json:"title"`
	Year    int      `json:"year"`
	Journal string   `json:"journal"`
	Volume  string   `json:"volume"`
	Issue   string   `json:"issue"`
	Pages   string   `json:"pages"`
	DOI     string   `json:"doi"`
	URL     string   `json:"url"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the misc endpoints.
type Handler struct {
	aiResolver *ai.ConfigResolver
}

// NewHandler creates a new Handler.
func NewHandler(aiResolver *ai.ConfigResolver) *Handler {
	return &Handler{aiResolver: aiResolver}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// POST /api/enhance
// ---------------------------------------------------------------------------

type enhanceRequest struct {
	Query    string `json:"query"`
	Text     string `json:"text"`     // alias for Query (Node.js compat)
	Type     string `json:"type"`     // "rewrite" | "expand" | "spell"
	Method   string `json:"method"`   // alias for Type (Node.js compat)
	KbID     string `json:"kbId"`     // optional
	Language string `json:"language"` // "de" | "en"
}

type enhanceResponse struct {
	Original string `json:"original"`
	Enhanced string `json:"enhanced"`
}

// Enhance handles POST /api/enhance.
// It requires auth and is expected to sit behind a rate limiter in the router.
func (h *Handler) Enhance(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req enhanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Accept both "query" and "text" fields; prefer "query".
	q := strings.TrimSpace(req.Query)
	if q == "" {
		q = strings.TrimSpace(req.Text)
	}
	if q == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "query is required")
		return
	}

	// Accept both "type" and "method" fields; prefer "type".
	enhanceType := strings.TrimSpace(req.Type)
	if enhanceType == "" {
		enhanceType = strings.TrimSpace(req.Method)
	}

	lang := req.Language
	if lang == "" {
		lang = "en"
	}

	ctx := r.Context()
	var enhanced string
	var err error

	switch enhanceType {
	case "rewrite":
		enhanced, err = ai.RewriteQuery(ctx, h.aiResolver, q, req.KbID, lang)
	case "expand":
		enhanced, err = ai.ExpandQuery(ctx, h.aiResolver, q, req.KbID, lang)
	case "spell":
		enhanced, err = ai.SpellCorrect(ctx, h.aiResolver, q, req.KbID, lang)
	default:
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "type must be one of: rewrite, expand, spell")
		return
	}

	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "enhancement failed")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, enhanceResponse{Original: q, Enhanced: enhanced})
}

// ---------------------------------------------------------------------------
// POST /api/kb/{id}/export/docx
// ---------------------------------------------------------------------------

type exportDocxRequest struct {
	Report string `json:"report"`
	Goal   string `json:"goal"`
}

// ExportDocx handles POST /api/kb/{id}/export/docx.
// Takes the report markdown and goal directly from the request body and
// returns a downloadable DOCX file. This matches the frontend contract
// (ResearchMode / AcademicResearchMode).
func (h *Handler) ExportDocx(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req exportDocxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Report == "" || req.Goal == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "report and goal are required")
		return
	}

	docxBytes := research.GenerateDocx(req.Goal, req.Report)

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="Research_Report.docx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docxBytes)
}

// ---------------------------------------------------------------------------
// POST /api/research/{sessionId}/bibtex
// ---------------------------------------------------------------------------

type researchBibtexRequest struct {
	Findings []Finding `json:"findings"`
}

// ResearchBibtex handles POST /api/research/{sessionId}/bibtex.
func (h *Handler) ResearchBibtex(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req researchBibtexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	bib := GenerateResearchBibtex(req.Findings)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=citations.bib")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, bib)
}

// ---------------------------------------------------------------------------
// POST /api/academic-research/{sessionId}/bibtex
// ---------------------------------------------------------------------------

type academicBibtexRequest struct {
	Papers []AcademicPaper `json:"papers"`
}

// AcademicBibtex handles POST /api/academic-research/{sessionId}/bibtex.
func (h *Handler) AcademicBibtex(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req academicBibtexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	bib := GenerateAcademicBibtex(req.Papers)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=academic_citations.bib")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, bib)
}

// ---------------------------------------------------------------------------
// BibTeX generation — research
// ---------------------------------------------------------------------------

// nonAlphanumRe matches any character that is not a letter, digit, or hyphen.
var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9\-]`)

// sanitizeFilename converts a filename into a BibTeX-safe string by replacing
// spaces and non-alphanumeric characters with underscores and lowercasing.
func sanitizeFilename(name string) string {
	// Strip extension.
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[:idx]
	}
	name = strings.ToLower(name)
	// Replace any non-alphanumeric/hyphen character with underscore.
	name = nonAlphanumRe.ReplaceAllString(name, "_")
	// Collapse repeated underscores.
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}
	name = strings.Trim(name, "_")
	if name == "" {
		name = "source"
	}
	return name
}

// shortHash returns the first 4 hex characters of a SHA-256 hash of s.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:2])[:4]
}

// GenerateResearchBibtex builds a BibTeX string from a list of findings.
// Sources are deduplicated by FileID; entries with a URL use @online, others @misc.
func GenerateResearchBibtex(findings []Finding) string {
	today := time.Now().UTC().Format("2006-01-02")

	// Deduplicate by FileID while preserving first-seen order.
	seen := make(map[string]struct{})
	var unique []Source
	for _, f := range findings {
		for _, s := range f.Sources {
			if s.FileID == "" {
				continue
			}
			if _, ok := seen[s.FileID]; ok {
				continue
			}
			seen[s.FileID] = struct{}{}
			unique = append(unique, s)
		}
	}

	var sb strings.Builder
	for _, s := range unique {
		key := sanitizeFilename(s.FileName) + "_" + shortHash(s.FileID)
		if s.URL != "" {
			fmt.Fprintf(&sb, "@online{%s,\n", key)
			fmt.Fprintf(&sb, "  title = {%s},\n", escapeLaTeX(s.FileName))
			fmt.Fprintf(&sb, "  url = {%s},\n", s.URL)
			fmt.Fprintf(&sb, "  urldate = {%s},\n", today)
			fmt.Fprintf(&sb, "  note = {Accessed via JustRAG}\n")
			sb.WriteString("}\n\n")
		} else {
			fmt.Fprintf(&sb, "@misc{%s,\n", key)
			fmt.Fprintf(&sb, "  title = {%s},\n", escapeLaTeX(s.FileName))
			fmt.Fprintf(&sb, "  note = {Accessed via JustRAG}\n")
			sb.WriteString("}\n\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// BibTeX generation — academic
// ---------------------------------------------------------------------------

// firstWord returns the first word of a string, lowercased and stripped of
// non-letter characters — suitable for use in a citation key.
func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "untitled"
	}
	// Split on whitespace.
	parts := strings.Fields(s)
	w := parts[0]
	// Keep only letters.
	var buf strings.Builder
	for _, r := range w {
		if unicode.IsLetter(r) {
			buf.WriteRune(unicode.ToLower(r))
		}
	}
	if buf.Len() == 0 {
		return "untitled"
	}
	return buf.String()
}

// authorSurname extracts the surname from an author string.
// Handles "First Last" and "Last, First" formats.
func authorSurname(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return "unknown"
	}
	// "Last, First" format.
	if idx := strings.Index(author, ","); idx > 0 {
		return strings.TrimSpace(author[:idx])
	}
	// "First ... Last" format.
	parts := strings.Fields(author)
	if len(parts) == 0 {
		return "unknown"
	}
	return parts[len(parts)-1]
}

// GenerateAcademicBibtex builds a BibTeX string from a list of academic papers.
// Papers are deduplicated by ID. Articles with a journal use @article; others use @misc.
// Keys are disambiguated with a/b/c suffixes when they collide.
func GenerateAcademicBibtex(papers []AcademicPaper) string {
	today := time.Now().UTC().Format("2006-01-02")

	// Deduplicate by ID while preserving first-seen order.
	seen := make(map[string]struct{})
	var unique []AcademicPaper
	for _, p := range papers {
		id := p.ID
		if id == "" {
			id = p.Title // fall back to title when no ID
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, p)
	}

	// Build citation keys with disambiguation.
	keyCount := make(map[string]int)
	keys := make([]string, len(unique))
	for i, p := range unique {
		surname := authorSurname(firstAuthor(p.Authors))
		yearStr := ""
		if p.Year > 0 {
			yearStr = fmt.Sprintf("%d", p.Year)
		}
		word := firstWord(p.Title)
		base := strings.ToLower(surname) + yearStr + word
		// Sanitize: keep only letters and digits.
		var cleanBuf strings.Builder
		for _, r := range base {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				cleanBuf.WriteRune(r)
			}
		}
		base = cleanBuf.String()
		if base == "" {
			base = "entry"
		}
		keyCount[base]++
		keys[i] = base
	}

	// Assign disambiguation suffixes where needed.
	// Build a map from base key → list of indices that share it.
	baseToIndices := make(map[string][]int)
	for i, k := range keys {
		baseToIndices[k] = append(baseToIndices[k], i)
	}
	for base, indices := range baseToIndices {
		if len(indices) <= 1 {
			continue
		}
		// Sort indices for deterministic output.
		sort.Ints(indices)
		for j, idx := range indices {
			suffix := string(rune('a' + j))
			keys[idx] = base + suffix
		}
	}

	var sb strings.Builder
	for i, p := range unique {
		key := keys[i]
		entryType := "@misc"
		if strings.TrimSpace(p.Journal) != "" {
			entryType = "@article"
		}

		fmt.Fprintf(&sb, "%s{%s,\n", entryType, key)

		if len(p.Authors) > 0 {
			// Join with " and " as per BibTeX convention.
			escaped := make([]string, len(p.Authors))
			for j, a := range p.Authors {
				escaped[j] = escapeLaTeX(a)
			}
			fmt.Fprintf(&sb, "  author = {%s},\n", strings.Join(escaped, " and "))
		}

		if p.Title != "" {
			fmt.Fprintf(&sb, "  title = {%s},\n", escapeLaTeX(p.Title))
		}

		if p.Year > 0 {
			fmt.Fprintf(&sb, "  year = {%d},\n", p.Year)
		}

		if p.Journal != "" {
			fmt.Fprintf(&sb, "  journal = {%s},\n", escapeLaTeX(p.Journal))
		}

		if p.Volume != "" {
			fmt.Fprintf(&sb, "  volume = {%s},\n", escapeLaTeX(p.Volume))
		}

		if p.Issue != "" {
			fmt.Fprintf(&sb, "  number = {%s},\n", escapeLaTeX(p.Issue))
		}

		if p.Pages != "" {
			fmt.Fprintf(&sb, "  pages = {%s},\n", escapeLaTeX(p.Pages))
		}

		if p.DOI != "" {
			fmt.Fprintf(&sb, "  doi = {%s},\n", p.DOI)
		}

		if p.URL != "" {
			fmt.Fprintf(&sb, "  url = {%s},\n", p.URL)
			fmt.Fprintf(&sb, "  urldate = {%s},\n", today)
		}

		sb.WriteString("}\n\n")
	}

	// Remove the trailing comma from the last field of each entry so BibTeX is valid.
	result := fixTrailingCommas(sb.String())
	return strings.TrimRight(result, "\n")
}

// firstAuthor returns the first element of authors, or empty string.
func firstAuthor(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	return authors[0]
}

// fixTrailingCommas removes the trailing comma from the last field inside each
// BibTeX entry so the output is spec-compliant. It works by finding lines that
// end with a comma immediately before a line containing only '}'.
func fixTrailingCommas(bib string) string {
	lines := strings.Split(bib, "\n")
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "}" {
			// Previous non-empty line should have its trailing comma removed.
			for j := i - 1; j >= 0; j-- {
				if strings.TrimSpace(lines[j]) != "" {
					lines[j] = strings.TrimRight(lines[j], ",")
					break
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// LaTeX special-character escaping
// ---------------------------------------------------------------------------

// latexReplacer escapes LaTeX special characters in BibTeX field values.
// The order matters: backslash must come first to avoid double-escaping.
var latexReplacer = strings.NewReplacer(
	`\`, `\textbackslash{}`,
	`&`, `\&`,
	`%`, `\%`,
	`$`, `\$`,
	`#`, `\#`,
	`_`, `\_`,
	`{`, `\{`,
	`}`, `\}`,
	`~`, `\textasciitilde{}`,
	`^`, `\textasciicircum{}`,
)

// escapeLaTeX escapes special LaTeX characters in s.
func escapeLaTeX(s string) string {
	return latexReplacer.Replace(s)
}
