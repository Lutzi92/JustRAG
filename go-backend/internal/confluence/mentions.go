package confluence

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

// acLinkUserRe matches a Confluence user-mention macro in storage format:
//
//	<ac:link><ri:user ri:userkey="abc"/></ac:link>
//	<ac:link><ri:user ri:account-id="123"/></ac:link>
//	<ac:link><ri:user ri:userkey="abc"/><ac:plain-text-link-body><![CDATA[@Name]]></ac:plain-text-link-body></ac:link>
//
// Captures:
//
//	[1] attribute name — "userkey" or "account-id"
//	[2] key/id value
//	[3] optional plain-text-link-body inner content (may include CDATA wrappers)
var acLinkUserRe = regexp.MustCompile(
	`(?s)<ac:link>\s*<ri:user\s+ri:(userkey|account-id)="([^"]+)"\s*/>\s*(?:<ac:plain-text-link-body>(.*?)</ac:plain-text-link-body>)?\s*</ac:link>`,
)

// cdataRe strips the <![CDATA[ ... ]]> wrapper from a plain-text-link-body.
var cdataRe = regexp.MustCompile(`(?s)^\s*<!\[CDATA\[(.*?)\]\]>\s*$`)

// userResolver is the minimal interface resolveUserMentions needs.
// *ConfluenceClient satisfies this via LookupUser.
type userResolver interface {
	LookupUser(ctx context.Context, key string) (string, error)
}

// resolveUserMentions replaces Confluence user-mention macros in html with
// "@DisplayName" text. Resolution is best-effort with this fallback order:
//
//  1. resolver.LookupUser succeeds  →  "@DisplayName"
//  2. mention carries plain-text-link-body →  that text verbatim
//  3. otherwise  →  "@<userkey or account-id>"
//
// A nil resolver is safe; steps 2 and 3 still apply. Results are cached within
// the call so repeated mentions of the same user cost one HTTP roundtrip.
//
// Returns the modified html plus the set of successfully-resolved display
// names (deduplicated, order preserved by first occurrence). The caller can
// use this list to emit a synthetic "mentioned persons" section that
// improves name-based retrieval recall — see importPage in sync.go.
func resolveUserMentions(ctx context.Context, html string, resolver userResolver) (string, []string) {
	matches := acLinkUserRe.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html, nil
	}

	type mention struct {
		start, end int
		key        string
		bodyText   string
	}
	items := make([]mention, 0, len(matches))
	for _, loc := range matches {
		m := mention{
			start: loc[0],
			end:   loc[1],
			key:   html[loc[4]:loc[5]],
		}
		if loc[6] >= 0 && loc[7] >= 0 {
			body := html[loc[6]:loc[7]]
			if sub := cdataRe.FindStringSubmatch(body); sub != nil {
				body = sub[1]
			}
			m.bodyText = strings.TrimSpace(body)
		}
		items = append(items, m)
	}

	// Resolve each unique key once.
	cache := make(map[string]string, len(items))
	for _, m := range items {
		if _, seen := cache[m.key]; seen {
			continue
		}
		if resolver == nil {
			cache[m.key] = ""
			continue
		}
		name, err := resolver.LookupUser(ctx, m.key)
		if err != nil {
			slog.Debug("confluence: user lookup failed", "key", m.key, "error", err)
			cache[m.key] = ""
			continue
		}
		cache[m.key] = strings.TrimSpace(name)
	}

	// Collect unique display names in first-seen order (stable output).
	resolved := make([]string, 0, len(cache))
	seenName := make(map[string]struct{}, len(cache))
	for _, m := range items {
		name := cache[m.key]
		if name == "" {
			continue
		}
		if _, dup := seenName[name]; dup {
			continue
		}
		seenName[name] = struct{}{}
		resolved = append(resolved, name)
	}

	// Apply replacements in reverse so earlier byte offsets stay valid.
	for i := len(items) - 1; i >= 0; i-- {
		m := items[i]
		var replacement string
		switch {
		case cache[m.key] != "":
			replacement = "@" + cache[m.key]
		case m.bodyText != "":
			replacement = m.bodyText
		default:
			replacement = "@" + m.key
		}
		html = html[:m.start] + replacement + html[m.end:]
	}
	return html, resolved
}
