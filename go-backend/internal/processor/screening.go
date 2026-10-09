package processor

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/promptsafety"
)

// screenedOrigins are the files.origin values whose parsed text goes through
// the ingest prompt-injection screen (W5-R8).
//
// User-added origins are deliberately absent: they are screened only in
// public KBs — see publicOnlyOrigins. Any other origin (e.g. "websearch",
// which no ingest path currently writes) is not screened: it would be
// agent-produced, answer-scoped content rather than corpus content.
var screenedOrigins = map[string]bool{
	"rss":        true,
	"confluence": true,
	"git":        true,
	"crawl":      true,
}

// publicOnlyOrigins are user-added origins (upload, text, url and "research",
// the academic import: third-party PDFs a user pulls into the corpus):
// screened only when the file
// lands in a PUBLIC KB, where it is third-party content for every other
// reader (user file library spec §11.2). In a private KB the uploader is
// the audience, and flagging their own text trains operators to ignore
// the badge.
var publicOnlyOrigins = map[string]bool{
	"upload":   true,
	"text":     true,
	"url":      true,
	"research": true,
}

// PublicOnlyOrigins returns the user-added origins in a stable order. The
// KB-publish screening job passes it to the files store, so the SQL filter
// and ShouldScreen cannot drift apart.
func PublicOnlyOrigins() []string {
	out := make([]string, 0, len(publicOnlyOrigins))
	for o := range publicOnlyOrigins {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// ShouldScreen reports whether a file of this origin, in a KB of this
// visibility, goes through the ingest prompt-injection screen.
func ShouldScreen(origin, kbVisibility string) bool {
	if screenedOrigins[origin] {
		return true
	}
	return publicOnlyOrigins[origin] && kbVisibility == "public"
}

// ScreeningStore is what ScreenAndRecord writes the verdict through.
// *files.PGStore satisfies it.
type ScreeningStore interface {
	SetInjectionFlag(ctx context.Context, fileID string, detail []byte) error
	MarkInjectionScreenedClean(ctx context.Context, fileID string, detail []byte) error
}

// ScreeningEnabled reports the ingest_screening_enabled kill switch.
func ScreeningEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return resolveScreeningEnabled(ctx, reader)
}

// injectionDetail is the shape persisted into files.injection_detail on a
// HIT. The Finding is embedded, so the JSON is flat: {rule, position,
// snippet, screened_at}.
type injectionDetail struct {
	promptsafety.Finding
	ScreenedAt time.Time `json:"screened_at"`
}

// screenedCleanDetail is what a pass that found NOTHING writes: the
// timestamp and nothing else. Deliberately not an injectionDetail with a
// zero Finding — that would persist rule:"" / position:0 / snippet:"", and
// a reader could not tell an empty rule from a real one.
//
// Together with injectionDetail these give files.injection_detail three
// readable states: NULL = never screened (ingested before the screen
// existed, an origin that is never screened, or the kill switch was off);
// {screened_at} = screened and clean; anything carrying "rule" = flagged.
// Without this shape, "clean" and "never screened" would both be NULL and
// an operator could not tell whether a badge-free file had been checked.
type screenedCleanDetail struct {
	ScreenedAt time.Time `json:"screened_at"`
}

// screenIfEligible runs the prompt-injection screen over a file's parsed
// text when its origin (and, for user-added origins, its KB's visibility)
// calls for it, and records the verdict on the files row. It is advisory in the
// strongest sense: it never blocks ingestion, never changes what is chunked,
// embedded or retrieved, and every one of its own failures is swallowed
// after a log line. The only effect of a hit is files.injection_flag and a
// badge in the admin UI.
//
// The origin and KB visibility are read from the store rather than threaded
// through ProcessFileInput: there are six ProcessFileInput construction
// sites and a missed one would silently disable screening for that source,
// which is the failure mode with no symptom. One extra small SELECT per file
// is a rounding error next to parsing it.
//
// Called after a successful parse and before chunking, on the non-spreadsheet
// branch only — a spreadsheet's "text" is a generated key:value render of
// typed cells, and the cell-level equivalent of this check already runs
// inside the sheet profiler (internal/promptsafety via tabular/profile).
func (p *Processor) screenIfEligible(ctx context.Context, fileID, text string) {
	if p.store == nil || !resolveScreeningEnabled(ctx, p.siteConfigReader) {
		return
	}
	origin, kbVisibility, err := p.store.GetFileScreeningInfo(ctx, fileID)
	if err != nil {
		logctx.From(ctx).Warn("processor: read screening info failed",
			"fileId", fileID, "error", err)
		return
	}
	if !ShouldScreen(origin, kbVisibility) {
		return
	}
	ScreenAndRecord(ctx, p.store, p.siteConfigReader, fileID, origin, text)
}

// ScreenAndRecord runs the screen over text and records the verdict on the
// files row. Callers have already applied the kill switch and ShouldScreen.
// Every failure is logged and swallowed — screening never blocks anything.
func ScreenAndRecord(ctx context.Context, store ScreeningStore, reader SiteConfigReader, fileID, origin, text string) {
	now := time.Now().UTC()
	finding, hit := promptsafety.ScreenText(text, resolveScreeningWindowRunes(ctx, reader))
	if !hit {
		// Record the clean verdict rather than leaving the row alone: it
		// drops a stale badge from a previous pass (the upstream page was
		// fixed, or the pattern set changed) AND it is what makes "screened,
		// clean" visible at all. The store makes the write conditional so an
		// already-clean row is not rewritten on every poll.
		clean, err := json.Marshal(screenedCleanDetail{ScreenedAt: now})
		if err != nil {
			logctx.From(ctx).Warn("processor: marshal clean screening detail failed",
				"fileId", fileID, "error", err)
			return
		}
		if err := store.MarkInjectionScreenedClean(ctx, fileID, clean); err != nil {
			logctx.From(ctx).Warn("processor: mark injection screened clean failed",
				"fileId", fileID, "error", err)
		}
		return
	}

	detail, err := json.Marshal(injectionDetail{Finding: finding, ScreenedAt: now})
	if err != nil {
		// Cannot happen for this shape, but a marshal failure must not
		// leave a stale flag behind either.
		logctx.From(ctx).Warn("processor: marshal injection detail failed",
			"fileId", fileID, "error", err)
		return
	}
	if err := store.SetInjectionFlag(ctx, fileID, detail); err != nil {
		logctx.From(ctx).Warn("processor: set injection flag failed",
			"fileId", fileID, "error", err)
		return
	}
	observability.RecordIngestInjectionFlag(origin)

	// Info, not Warn: a hit is expected background noise on a security-
	// advisory or documentation corpus (a page ABOUT prompt injection trips
	// every one of these rules), and a Warn-level stream of quoted attacker
	// text is both alert fatigue and an untrusted string in an operator's
	// terminal. The preview is bounded and the full snippet stays in the
	// jsonb column where the UI renders it as data.
	logctx.From(ctx).Info("ingest.injection.flagged",
		"file_id", fileID,
		"origin", origin,
		"rule", finding.Rule,
		"position", finding.Position,
		"preview", previewRunes(finding.Snippet, 120))
}

// previewRunes truncates s to at most n runes, appending an ellipsis when it
// cut. Rune-based so a German preview never ends mid-codepoint.
func previewRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// resolveScreeningEnabled gates the ingest prompt-injection screen
// (site_config: ingest_screening_enabled). Default ON — the screen is a flag,
// not a filter, so the safe default is "tell me", and the key exists as a
// kill switch for a deployment whose corpus is all false positives (a
// documentation KB about prompting) or that wants the extra SELECT gone.
// Mirrors resolveEnrichmentEnabled's parsing: only an explicit "false"/"0"
// turns it off.
func resolveScreeningEnabled(ctx context.Context, reader SiteConfigReader) bool {
	if reader == nil {
		return true
	}
	val, err := reader.GetSiteConfigValue(ctx, "ingest_screening_enabled")
	if err != nil {
		logctx.From(ctx).Warn("processor: failed to read site config",
			"key", "ingest_screening_enabled", "error", err)
		return true
	}
	if val == nil {
		return true
	}
	switch strings.TrimSpace(*val) {
	case "false", "0":
		return false
	default:
		return true
	}
}

const (
	screeningWindowDefault = 600
	screeningWindowMin     = 100
	screeningWindowMax     = 5000
)

// resolveScreeningWindowRunes sizes the sliding window the screen matches in
// (site_config: ingest_screening_window_runes, default 600, clamped to
// [100, 5000]). Clamped rather than rejected: an out-of-range value must not
// silently disable screening, and a window below the longest pattern would
// do exactly that. An unparseable value falls back to the default.
func resolveScreeningWindowRunes(ctx context.Context, reader SiteConfigReader) int {
	if reader == nil {
		return screeningWindowDefault
	}
	val, err := reader.GetSiteConfigValue(ctx, "ingest_screening_window_runes")
	if err != nil || val == nil {
		return screeningWindowDefault
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(*val))
	if convErr != nil {
		logctx.From(ctx).Warn("processor: invalid site config value; using default",
			"key", "ingest_screening_window_runes", "default", screeningWindowDefault)
		return screeningWindowDefault
	}
	if n < screeningWindowMin {
		return screeningWindowMin
	}
	if n > screeningWindowMax {
		return screeningWindowMax
	}
	return n
}
