package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// LongContextFinding is one extracted claim from the map stage of the
// long-context map-reduce consumer.
//
// SourceIdx is the 1-based `[N]` number of the chunk the claim came from, in
// the numbering buildChatSourcesAndContext assigned over the WHOLE chunk pool
// (not the group). Keeping that domain is what lets the reduce stage emit
// citations the existing validator and the `Sources` list still understand.
type LongContextFinding struct {
	SourceIdx int    `json:"source_idx"`
	Claim     string `json:"claim"`
	Quote     string `json:"quote"`
}

// longContextFindingsSpec is the strict Structured-Outputs contract (W3-R6).
var longContextFindingsSpec = &StructuredSpec{
	Name: "longcontext_findings",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"findings": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"source_idx": {"type": "integer"},
						"claim": {"type": "string"},
						"quote": {"type": "string"}
					},
					"required": ["source_idx", "claim", "quote"],
					"additionalProperties": false
				}
			}
		},
		"required": ["findings"],
		"additionalProperties": false
	}`),
}

// maxLongContextQuoteRunes bounds a returned quote. The prompt already asks
// for ≤ 240 characters; this is the defensive trim so one runaway group
// cannot blow up the reduce context.
const maxLongContextQuoteRunes = 240

// ExtractLongContextFindings runs one fast-tier structured call over a group
// of numbered passages and returns the claims they support.
//
// A transport error bubbles so the caller can apply its raw-chunk fallback
// (W3-R7). Unparseable output is NOT an error: it yields an empty slice, which
// the caller treats as "this group had nothing to say".
func ExtractLongContextFindings(ctx context.Context, resolver *ConfigResolver, question, groupText, kbID, lang, modelOverride string) ([]LongContextFinding, error) {
	user := buildLongContextFindingsUserPrompt(question, groupText)
	res, err := resolver.structuredCompletionFn(ctx, user, prompts.LongContextFindingsPrompt(lang), kbID, modelOverride, longContextFindingsSpec)
	if err != nil {
		return nil, err
	}
	return parseLongContextFindings(res.Content), nil
}

func buildLongContextFindingsUserPrompt(question, groupText string) string {
	return fmt.Sprintf("Question:\n%s\n\nPassages:\n%s", strings.TrimSpace(question), groupText)
}

// parseLongContextFindings is tolerant of the json_object downgrade path:
// prose-wrapped objects and a bare top-level array both parse. Entries with an
// empty claim are dropped; claim and quote are trimmed and the quote capped.
func parseLongContextFindings(text string) []LongContextFinding {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	type envelope struct {
		Findings []LongContextFinding `json:"findings"`
	}

	var raw []LongContextFinding
	var env envelope
	if err := json.Unmarshal([]byte(text), &env); err == nil && env.Findings != nil {
		raw = env.Findings
	} else if err := json.Unmarshal([]byte(text), &raw); err != nil {
		raw = nil
		// Retry on the substring between the outermost braces / brackets —
		// mid-tier models occasionally prepend "Here is the JSON:".
		if s, e := strings.Index(text, "{"), strings.LastIndex(text, "}"); s >= 0 && e > s {
			if jerr := json.Unmarshal([]byte(text[s:e+1]), &env); jerr == nil {
				raw = env.Findings
			}
		}
		if raw == nil {
			if s, e := strings.Index(text, "["), strings.LastIndex(text, "]"); s >= 0 && e > s {
				_ = json.Unmarshal([]byte(text[s:e+1]), &raw)
			}
		}
	}

	out := make([]LongContextFinding, 0, len(raw))
	for _, f := range raw {
		claim := strings.TrimSpace(f.Claim)
		if claim == "" {
			continue
		}
		quote := strings.TrimSpace(f.Quote)
		if r := []rune(quote); len(r) > maxLongContextQuoteRunes {
			quote = string(r[:maxLongContextQuoteRunes])
		}
		out = append(out, LongContextFinding{SourceIdx: f.SourceIdx, Claim: claim, Quote: quote})
	}
	return out
}
