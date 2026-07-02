package chat

import (
	"regexp"
	"strconv"
	"strings"
)

// Recency-listing query detection.
//
// "Welche neuen Meldungen gibt es?" is a temporal listing question, not a
// topical one: the query text carries almost no semantic signal, so the
// BM25 + vector arms return an arbitrary subset of the corpus and the
// enumeration pre-pass (which only enforces completeness over the
// retrieved context) cannot recover the recently ingested files that
// never made top-k. Observed in production 2026-07-02: one of many "NEU"
// advisories listed.
//
// When this classifier fires, PrepareChatContext window-scopes retrieval
// (SearchOptions.CreatedAfter) and injects the complete file listing for
// the window as a system-prompt addendum (see recency_listing.go).
//
// Precision over recall, like IsEnumerationQuery: the patterns anchor the
// recency adjective to a document-ish noun ("neue Meldungen", "latest
// advisories") or to an ingest verb ("zuletzt hinzugefügt", "recently
// added"), so "neue Erkenntnisse" / "new features" — "neu" as *novel*,
// not *recently ingested* — do not fire. Both language lists are checked
// regardless of conversation language (mirrors IsGlobalSynthesisQuery).

// docNounsDE / docNounsEN are the "document-ish" nouns a recency
// adjective must attach to.
const docNounsDE = `(?:meldungen|nachrichten|artikel|news|dokumente|dateien|einträge|eintraege|advisories|warnungen|beiträge|beitraege|veröffentlichungen|veroeffentlichungen|publikationen|berichte|inhalte)`
const docNounsEN = `(?:news|articles?|documents?|files?|items?|entries|advisories|alerts?|posts?|reports?|publications?|announcements?|content)`

var recencyListingPatterns = []*regexp.Regexp{
	// DE: recency adjective directly before a document noun.
	regexp.MustCompile(`(?i)\b(?:neue|neuen|neueste|neuesten|aktuelle|aktuellen|frische|frischen)\s+` + docNounsDE + `\b`),
	// DE: standalone "what's new" phrasings.
	regexp.MustCompile(`(?i)\bwas\s+gibt\s+es\s+neues\b`),
	regexp.MustCompile(`(?i)\bwas\s+ist\s+(?:seit\s+\S+\s+)?neu\b`),
	// DE: ingest verbs with a recency adverb.
	regexp.MustCompile(`(?i)\b(?:zuletzt|kürzlich|kuerzlich|gerade|neu)\s+(?:hinzugefügt|hinzugefuegt|hinzugekommen|eingestellt|aufgenommen|veröffentlicht|veroeffentlicht|erschienen)\b`),
	// DE: explicit time window + document noun anywhere in the query
	// ("Welche Artikel wurden in den letzten 5 Tagen veröffentlicht?").
	regexp.MustCompile(`(?i)\b(?:letzten?|vergangenen?)\s+\d{1,3}\s+(?:tagen?|wochen?|monaten?)\b[\s\S]*\b` + docNounsDE + `\b`),
	regexp.MustCompile(`(?i)\b` + docNounsDE + `\b[\s\S]*\b(?:letzten?|vergangenen?)\s+\d{1,3}\s+(?:tagen?|wochen?|monaten?)\b`),
	// DE: document noun + heute/gestern ("Welche Meldungen gibt es von heute?").
	regexp.MustCompile(`(?i)\b` + docNounsDE + `\b[\s\S]*\b(?:heute|gestern|vorgestern)\b`),
	// EN: recency adjective directly before a document noun.
	regexp.MustCompile(`(?i)\b(?:new|newest|recent|latest|fresh)\s+` + docNounsEN + `\b`),
	// EN: standalone "what's new".
	regexp.MustCompile(`(?i)\bwhat(?:'s|\s+is)\s+new\b`),
	// EN: ingest verbs with a recency adverb.
	regexp.MustCompile(`(?i)\b(?:recently|just|newly|lately)\s+(?:added|ingested|uploaded|published|posted)\b`),
	// EN: explicit time window + document noun anywhere in the query.
	regexp.MustCompile(`(?i)\b(?:last|past)\s+\d{1,3}\s+(?:days?|weeks?|months?)\b[\s\S]*\b` + docNounsEN + `\b`),
	regexp.MustCompile(`(?i)\b` + docNounsEN + `\b[\s\S]*\b(?:last|past)\s+\d{1,3}\s+(?:days?|weeks?|months?)\b`),
}

// IsRecencyListingQuery reports whether the query asks "what is new /
// recently added" — i.e. a listing over ingest recency rather than a
// topical question. Checks German and English patterns regardless of
// conversation language.
func IsRecencyListingQuery(query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	for _, re := range recencyListingPatterns {
		if re.MatchString(q) {
			return true
		}
	}
	return false
}

// reNewMarkerMention detects that the query literally uses "neu"/"new"
// (any inflection). Some corpora — CERT-Bund advisories — carry "NEU" /
// "UPDATE" as a status label in the file NAME, so "neue Meldungen" can
// target the labeled subset rather than ingest recency. Only when the
// query actually says "neu"/"new" is a name-marker lookup justified;
// "aktuelle Warnungen" or "zuletzt hinzugefügt" are purely temporal.
var reNewMarkerMention = regexp.MustCompile(`(?i)\b(?:neu\w*|new|newest)\b`)

func queryMentionsNewMarker(query string) bool {
	return reNewMarkerMention.MatchString(query)
}

// Explicit-window extraction: "in den letzten 5 Tagen" / "last 10 days"
// override the configured default window (chat_recency_listing_window_days).
var (
	reWindowDE = regexp.MustCompile(`(?i)\b(?:letzten?|vergangenen?)\s+(\d{1,3})\s+(tagen?|wochen?|monaten?)\b`)
	reWindowEN = regexp.MustCompile(`(?i)\b(?:last|past)\s+(\d{1,3})\s+(days?|weeks?|months?)\b`)
	reToday    = regexp.MustCompile(`(?i)\b(?:heute|today)\b`)
	reYesterday = regexp.MustCompile(`(?i)\b(?:gestern|yesterday)\b`)
)

// explicitRecencyWindowDays extracts an explicit relative window from the
// query, in days. ok=false means the query names no explicit window and
// the configured default applies. Units: Wochen/weeks ×7, Monate/months
// ×30; "heute/today" = 0 (start of today), "gestern/yesterday" = 1.
func explicitRecencyWindowDays(query string) (int, bool) {
	for _, re := range []*regexp.Regexp{reWindowDE, reWindowEN} {
		m := re.FindStringSubmatch(query)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		unit := strings.ToLower(m[2])
		switch {
		case strings.HasPrefix(unit, "woche"), strings.HasPrefix(unit, "week"):
			n *= 7
		case strings.HasPrefix(unit, "monat"), strings.HasPrefix(unit, "month"):
			n *= 30
		}
		return n, true
	}
	if reYesterday.MatchString(query) {
		return 1, true
	}
	if reToday.MatchString(query) {
		return 0, true
	}
	return 0, false
}
