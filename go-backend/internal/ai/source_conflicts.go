package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
)

// maxDetectedConflicts caps how many conflict pairs one call may report
// (W5-R7). The addendum is a prompt block the answer LLM has to obey; past
// a handful of entries it stops being a hint and starts crowding out the
// context itself, and a model that returns dozens of "conflicts" for one
// retrieval set is over-reporting rather than finding more.
const maxDetectedConflicts = 10

// maxConflictClaimRunes caps one claim. The claim is model-authored free
// text that is persisted on the message and rendered into the ANSWER system
// prompt; it is meant to be one sentence naming what the two sources
// disagree about. Capping it here — at the only place the value is created —
// means every downstream consumer (the JSONB blob, the SSE frame, the FE
// badge, the prompt addendum) inherits the bound instead of having to
// re-derive it. The addendum applies the same 300 to text that reaches it by
// any other route.
const maxConflictClaimRunes = 300

// conflictKinds / conflictNewer are the closed value sets the parser
// accepts. Anything else drops the row rather than reaching the prompt: a
// downstream addendum built from an unrecognised kind would render a
// sentence that claims something the model never said.
var conflictKinds = map[string]bool{"contradiction": true, "superseded": true}
var conflictNewer = map[string]bool{"a": true, "b": true, "unknown": true}

// ConflictSource is one numbered source handed to the detector: Idx is the
// citation number the answer prompt uses for this chunk ([N]), so a
// returned SourceA/SourceB can be resolved straight back to a source
// without a second mapping.
type ConflictSource struct {
	Idx      int
	Name     string
	DateLine string
	Content  string
}

// Conflict is one disagreement between two of the numbered sources.
type Conflict struct {
	Claim   string `json:"claim"`
	SourceA int    `json:"source_a"`
	SourceB int    `json:"source_b"`
	// Kind is "contradiction" (both stand, incompatible) or "superseded"
	// (the newer source replaces the older).
	Kind string `json:"kind"`
	// Newer is "a", "b" or "unknown" — decided from the date lines only.
	Newer string `json:"newer"`
}

// ConflictFindings is the detector's whole result.
type ConflictFindings struct {
	Conflicts []Conflict `json:"conflicts"`
}

// sourceConflictSpec is the Structured-Outputs contract for
// DetectSourceConflicts.
var sourceConflictSpec = &StructuredSpec{
	Name: "source_conflicts",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"conflicts": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"claim": {"type": "string"},
						"source_a": {"type": "integer"},
						"source_b": {"type": "integer"},
						"kind": {"type": "string", "enum": ["contradiction", "superseded"]},
						"newer": {"type": "string", "enum": ["a", "b", "unknown"]}
					},
					"required": ["claim", "source_a", "source_b", "kind", "newer"],
					"additionalProperties": false
				}
			}
		},
		"required": ["conflicts"],
		"additionalProperties": false
	}`),
}

// truncateRunesTo cuts s to at most max runes. Unlike truncateForLog it
// counts RUNES and appends nothing: the result is stored and rendered, not
// logged, and a byte cut would split a German multi-byte rune.
func truncateRunesTo(s string, max int) string {
	if max <= 0 || len(s) <= max {
		// len(s) <= max in bytes implies <= max runes.
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// DetectSourceConflicts asks a fast-tier LLM which of the supplied numbered
// sources disagree with each other, and which of a disagreeing pair is the
// newer document (W5-R7).
//
// modelOverride routes the call to a fast-tier model (resolved by the
// caller via chat.ResolveFastTierModel); empty falls back to the KB's
// ChatModel.
//
// Unlike JudgeContextSufficiency this does NOT fail open: an LLM or parse
// error returns an error and the caller surfaces nothing. A conflict badge
// is an assertion about the corpus — inventing one on a flaky judge is
// strictly worse than staying silent.
//
// Returned rows are validated against the input numbering: an index the
// model made up, a self-pair, an unknown kind/newer value or an empty claim
// is dropped, and the list is capped at maxDetectedConflicts. A response
// whose rows are ALL invalid yields an empty (non-nil) findings value, not
// an error — the model answered, it just said nothing usable.
func DetectSourceConflicts(ctx context.Context, resolver *ConfigResolver, kbID, question string, sources []ConflictSource, lang, modelOverride string) (*ConflictFindings, error) {
	if len(sources) < 2 {
		return nil, fmt.Errorf("ai: source conflicts need at least 2 sources, got %d", len(sources))
	}

	blocks := make([]prompts.SourceConflictBlock, len(sources))
	valid := make(map[int]bool, len(sources))
	for i, s := range sources {
		blocks[i] = prompts.SourceConflictBlock{
			Idx:      s.Idx,
			Name:     s.Name,
			DateLine: s.DateLine,
			Content:  s.Content,
		}
		valid[s.Idx] = true
	}

	res, err := resolver.structuredCompletionFn(ctx,
		prompts.SourceConflictUser(lang, question, blocks),
		prompts.SourceConflictSystem(lang),
		kbID, modelOverride, sourceConflictSpec)
	if err != nil {
		return nil, fmt.Errorf("ai: source conflict call: %w", err)
	}

	var parsed ConflictFindings
	text := stripJSONFences(strings.TrimSpace(res.Content))
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		if err2 := parseJSONFromText(text, &parsed); err2 != nil {
			logctx.From(ctx).Warn("ai.source_conflicts.parse_failed",
				"content_preview", truncateForLog(res.Content, 200), "kbId", kbID)
			return nil, fmt.Errorf("ai: source conflict parse: %w", err)
		}
	}

	out := &ConflictFindings{}
	for _, c := range parsed.Conflicts {
		if len(out.Conflicts) >= maxDetectedConflicts {
			break
		}
		c.Claim = truncateRunesTo(strings.TrimSpace(c.Claim), maxConflictClaimRunes)
		if c.Claim == "" {
			continue
		}
		if c.SourceA == c.SourceB || !valid[c.SourceA] || !valid[c.SourceB] {
			continue
		}
		if !conflictKinds[c.Kind] || !conflictNewer[c.Newer] {
			continue
		}
		out.Conflicts = append(out.Conflicts, c)
	}
	return out, nil
}
