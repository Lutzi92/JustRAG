package canonicalize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SiteConfigReader is the minimal site-config surface for the canon readers.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

func readString(ctx context.Context, r SiteConfigReader, key string) string {
	if r == nil {
		return ""
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil || v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// Enabled gates the whole feature (endpoint 503 + handler no-op). Default off.
func Enabled(ctx context.Context, r SiteConfigReader) bool {
	return strings.EqualFold(readString(ctx, r, "kg_canonicalization_enabled"), "true")
}

// Threshold is the cosine cutoff for candidate pairs. Default 0.85, clamped to (0,1).
func Threshold(ctx context.Context, r SiteConfigReader) float64 {
	if v := readString(ctx, r, "kg_canonicalization_threshold"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil && f > 0 && f < 1 {
			return f
		}
	}
	return 0.85
}

// MaxPairs caps candidate pairs sent to the LLM. Default 200, clamped to [1,5000].
func MaxPairs(ctx context.Context, r SiteConfigReader) int {
	if v := readString(ctx, r, "kg_canonicalization_max_pairs"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 1 && n <= 5000 {
			return n
		}
	}
	return 200
}

// confirmResult is the strict structured-output contract.
type confirmResult struct {
	Verdicts []struct {
		Index int  `json:"index"`
		Same  bool `json:"same"`
	} `json:"verdicts"`
}

// ConfirmSchema is the json_schema for the confirm call.
const ConfirmSchema = `{
  "type":"object",
  "properties":{"verdicts":{"type":"array","items":{
    "type":"object",
    "properties":{"index":{"type":"integer"},"same":{"type":"boolean"}},
    "required":["index","same"],"additionalProperties":false}}},
  "required":["verdicts"],"additionalProperties":false
}`

// ConfirmSystemPrompt instructs the judge.
const ConfirmSystemPrompt = "You decide whether two knowledge-graph entities refer to the SAME real-world thing " +
	"(e.g. 'PPM' and 'Projektmanagement' -> same; 'John Smith' and 'Jane Smith' -> different). " +
	"Return a verdict per indexed pair. Be conservative: only 'same' when clearly the same entity."

// BuildConfirmPrompt renders the candidate pairs for the judge.
func BuildConfirmPrompt(ents []CanonEntity, pairs []Pair) string {
	var b strings.Builder
	b.WriteString("Candidate entity pairs (decide same vs different):\n")
	for idx, p := range pairs {
		fmt.Fprintf(&b, "%d: [%s] \"%s\"  vs  \"%s\"\n", idx, ents[p.I].Type, ents[p.I].Name, ents[p.J].Name)
	}
	b.WriteString("\nReturn verdicts: [{index, same}].")
	return b.String()
}

// ParseConfirm maps the LLM JSON to the confirmed subset of pairs.
func ParseConfirm(raw string, pairs []Pair) ([]Pair, error) {
	var res confirmResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &res); err != nil {
		return nil, err
	}
	var out []Pair
	for _, v := range res.Verdicts {
		if v.Same && v.Index >= 0 && v.Index < len(pairs) {
			out = append(out, pairs[v.Index])
		}
	}
	return out, nil
}
