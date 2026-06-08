package ai

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// CorpusColumn is one column in a corpus-comparison table, as proposed by the
// planner or stated by the user. Mirrors chat.TableColumn but lives in the ai
// package to avoid an import cycle (chat imports ai, not vice-versa).
type CorpusColumn struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Type       string `json:"type"` // string | number | date
	DeriveFrom string `json:"deriveFrom,omitempty"`
}

// --- 4a. Router confirmation -------------------------------------------------

type corpusConfirmResp struct {
	IsCorpusTable bool   `json:"is_corpus_table"`
	Reason        string `json:"reason"`
}

// ConfirmCorpusComparison is the precision stage of the two-stage router. It
// returns true only when the model agrees the query wants a corpus-wide
// comparison table. Fail-open is DENY (returns false on any error) — a missed
// positive degrades to the normal chat path, far cheaper than a false fire.
func ConfirmCorpusComparison(ctx context.Context, resolver *ConfigResolver, question, kbID, lang, modelOverride string) bool {
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return false
	}
	model := pickModel(cfg, modelOverride)
	ctx, span := observability.StartGenerationSpan(ctx, "rag.corpus_table.confirm", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(cfg.BaseURL), Model: model,
	})
	defer span.End()
	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: prompts.CorpusTableRouterSystem(lang)},
			{Role: "user", Content: question},
		},
		Temperature:    &temp,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	client := CachedClient(cfg.BaseURL, cfg.APIKey)
	resp, err := client.ChatCompletion(ctx, req)
	if err != nil || len(resp.Choices) == 0 {
		return false
	}
	var parsed corpusConfirmResp
	if json.Unmarshal([]byte(resp.Choices[0].Message.Content), &parsed) != nil {
		if parseJSONFromText(resp.Choices[0].Message.Content, &parsed) != nil {
			return false
		}
	}
	return parsed.IsCorpusTable
}

// --- 4b. Column planning -----------------------------------------------------

type corpusColumnsResp struct {
	Columns []CorpusColumn `json:"columns"`
}

// PlanCorpusColumns asks the model to propose comparison columns from the user
// request + a sample of in-scope documents. Returns a generic fallback set on
// any failure so the feature always produces a table.
func PlanCorpusColumns(ctx context.Context, resolver *ConfigResolver, question, sampleDocs, kbID, lang, modelOverride string) []CorpusColumn {
	fallback := []CorpusColumn{
		{Key: "name", Label: "Name", Type: "string"},
		{Key: "summary", Label: "Summary", Type: "string"},
		{Key: "key_facts", Label: "Key facts", Type: "string"},
	}
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return fallback
	}
	model := pickModel(cfg, modelOverride)
	ctx, span := observability.StartGenerationSpan(ctx, "rag.corpus_table.plan", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(cfg.BaseURL), Model: model,
	})
	defer span.End()
	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: prompts.CorpusTableColumnsSystem(lang)},
			{Role: "user", Content: prompts.CorpusTableColumnsUser(question, sampleDocs)},
		},
		Temperature:    &temp,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	client := CachedClient(cfg.BaseURL, cfg.APIKey)
	resp, err := client.ChatCompletion(ctx, req)
	if err != nil || len(resp.Choices) == 0 {
		return fallback
	}
	var parsed corpusColumnsResp
	if json.Unmarshal([]byte(resp.Choices[0].Message.Content), &parsed) != nil {
		if parseJSONFromText(resp.Choices[0].Message.Content, &parsed) != nil {
			return fallback
		}
	}
	if len(parsed.Columns) == 0 {
		return fallback
	}
	return parsed.Columns
}

// MergeUserColumns prepends user-stated columns (guaranteed present, in order)
// and drops any planned column whose key duplicates a user column key.
func MergeUserColumns(planned, user []CorpusColumn) []CorpusColumn {
	if len(user) == 0 {
		return planned
	}
	seen := make(map[string]struct{}, len(user))
	out := make([]CorpusColumn, 0, len(user)+len(planned))
	for _, c := range user {
		if _, dup := seen[c.Key]; dup {
			continue
		}
		seen[c.Key] = struct{}{}
		out = append(out, c)
	}
	for _, c := range planned {
		if _, dup := seen[c.Key]; dup {
			continue
		}
		seen[c.Key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// --- 4c. Per-file row extraction --------------------------------------------

// ExtractCorpusRow extracts the non-derived column values for one file's full
// text. cols MUST exclude filename-derived columns (the caller computes those
// in code). Returns a key->value map; missing values are "—". Fail-open returns
// an all-"—" map so one bad file never aborts the table.
func ExtractCorpusRow(ctx context.Context, resolver *ConfigResolver, question, fileName, fileText string, cols []CorpusColumn, kbID, lang, modelOverride string) map[string]any {
	out := make(map[string]any, len(cols))
	for _, c := range cols {
		out[c.Key] = "—"
	}
	if len(cols) == 0 {
		return out
	}
	// Truncate very long documents to avoid excessive latency.
	const maxDocRunes = 16000
	if r := []rune(fileText); len(r) > maxDocRunes {
		fileText = string(r[:maxDocRunes])
	}
	cfg, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return out
	}
	model := pickModel(cfg, modelOverride)
	ctx, span := observability.StartGenerationSpan(ctx, "rag.corpus_table.extract_row", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(cfg.BaseURL), Model: model,
	})
	defer span.End()
	start := time.Now()
	temp := 0.0
	req := &ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: prompts.CorpusTableExtractSystem(lang)},
			{Role: "user", Content: prompts.CorpusTableExtractUser(question, fileName, fileText, corpusColumnsJSON(cols))},
		},
		Temperature:    &temp,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client := CachedClient(cfg.BaseURL, cfg.APIKey)
	resp, err := client.ChatCompletion(callCtx, req)
	if err != nil || len(resp.Choices) == 0 {
		return out
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(resp.Choices[0].Message.Content), &parsed) != nil {
		if parseJSONFromText(resp.Choices[0].Message.Content, &parsed) != nil {
			return out
		}
	}
	for _, c := range cols {
		if v, ok := parsed[c.Key]; ok && v != nil && v != "" {
			out[c.Key] = v
		}
	}
	logctx.From(ctx).Info("rag.corpus_table.row_extracted",
		"file", fileName, "cols", len(cols), "duration_ms", time.Since(start).Milliseconds(), "model", model)
	return out
}

func corpusColumnsJSON(cols []CorpusColumn) string {
	b, _ := json.Marshal(cols)
	return string(b)
}

// --- shared helpers ----------------------------------------------------------

func pickModel(cfg *ResolvedConfig, override string) string {
	if override != "" && slices.Contains(cfg.ChatModels, override) {
		return override
	}
	return cfg.ChatModel
}
