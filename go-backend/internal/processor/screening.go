package processor

import (
	"context"
	"encoding/json"
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
// User uploads are deliberately absent: a user pasting instruction-shaped
// text into their own KB is not an attack on themselves, and flagging it
// would train operators to ignore the badge. "websearch" and "research" are
// absent too — those files are produced by an agent run the user triggered
// and are already answer-scoped, not corpus-scoped.
var screenedOrigins = map[string]bool{
	"rss":        true,
	"confluence": true,
	"git":        true,
	"crawl":      true,
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

// screenIfExternal runs the prompt-injection screen over a file's parsed
// text and records the verdict on the files row. It is advisory in the
// strongest sense: it never blocks ingestion, never changes what is chunked,
// embedded or retrieved, and every one of its own failures is swallowed
// after a log line. The only effect of a hit is files.injection_flag and a
// badge in the admin UI.
//
// The origin is read from the store rather than threaded through
// ProcessFileInput: there are six ProcessFileInput construction sites and a
// missed one would silently disable screening for that source, which is the
// failure mode with no symptom. One extra single-column SELECT per file is a
// rounding error next to parsing it.
//
// Called after a successful parse and before chunking, on the non-spreadsheet
// branch only — a spreadsheet's "text" is a generated key:value render of
// typed cells, and the cell-level equivalent of this check already runs
// inside the sheet profiler (internal/promptsafety via tabular/profile).
func (p *Processor) screenIfExternal(ctx context.Context, fileID, text string) {
	if p.store == nil || !resolveScreeningEnabled(ctx, p.siteConfigReader) {
		return
	}
	origin, err := p.store.GetFileOrigin(ctx, fileID)
	if err != nil {
		logctx.From(ctx).Warn("processor: read origin for injection screening failed",
			"fileId", fileID, "error", err)
		return
	}
	if !screenedOrigins[origin] {
		return
	}

	now := time.Now().UTC()
	finding, hit := promptsafety.ScreenText(text, resolveScreeningWindowRunes(ctx, p.siteConfigReader))
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
		if err := p.store.MarkInjectionScreenedClean(ctx, fileID, clean); err != nil {
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
	if err := p.store.SetInjectionFlag(ctx, fileID, detail); err != nil {
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
