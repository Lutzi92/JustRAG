package chat

import (
	"encoding/json"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chatattach"
)

// Finding aliases the store type so the engine and store share one definition.
type Finding = chatattach.Finding

// comparisonFindingsSpec returns the strict json_schema for one section+mode check.
// The model returns {"findings":[...]} (object wrapper — vLLM guided_json requires an
// object at top level, mirroring the existing decompose spec's pattern).
func comparisonFindingsSpec() *ai.StructuredSpec {
	return &ai.StructuredSpec{
		Name: "comparison_findings",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "severity":   {"type": "string", "enum": ["high","medium","low"]},
          "uploadQuote":{"type": "string"},
          "issue":      {"type": "string"},
          "citedFileIds":{"type": "array", "items": {"type": "string"}},
          "citedQuote": {"type": "string"}
        },
        "required": ["severity","uploadQuote","issue","citedFileIds","citedQuote"],
        "additionalProperties": false
      }
    }
  },
  "required": ["findings"],
  "additionalProperties": false
}`),
	}
}

type comparisonFindingsPayload struct {
	Findings []struct {
		Severity     string   `json:"severity"`
		UploadQuote  string   `json:"uploadQuote"`
		Issue        string   `json:"issue"`
		CitedFileIDs []string `json:"citedFileIds"`
		CitedQuote   string   `json:"citedQuote"`
	} `json:"findings"`
}
