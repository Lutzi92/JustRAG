package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/splitter"
)

// KGEntity is the AI-side representation of one extractor entity.
// Mirrors the LLM's JSON shape and the storage-side row before it
// gets dim-merged into kg_entities.
type KGEntity struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Aliases []string `json:"aliases,omitempty"`
}

// KGRelation is one (src, rel, dst) edge with an evidence quote.
type KGRelation struct {
	Src      string `json:"src"`
	Rel      string `json:"rel"`
	Dst      string `json:"dst"`
	Evidence string `json:"evidence,omitempty"`
}

// KGExtraction is the per-chunk extractor output. Slices may be
// empty — the LLM is instructed to emit `{"entities": [], "relations": []}`
// when the chunk is entity-free (boilerplate, page numbers, etc.).
type KGExtraction struct {
	Entities  []KGEntity   `json:"entities"`
	Relations []KGRelation `json:"relations"`
}

// kgExtractionSpec is the Structured-Outputs contract for ExtractKG,
// matching the prompt's {"entities":[...],"relations":[...]} shape.
// The type enum mirrors prompts.AllowedKGEntityTypes (closed set);
// the post-parse normalizer still coerces drift to "other" on the
// json_object fallback path. rel stays free-form — the prompt asks
// for an open snake_case verb phrase, not a closed vocabulary.
var kgExtractionSpec = &StructuredSpec{
	Name: "kg_extraction",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"entities": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"type": {"type": "string", "enum": ["person", "organization", "project", "document", "concept", "location", "role", "other"]},
						"aliases": {"type": "array", "items": {"type": "string"}}
					},
					"required": ["name", "type", "aliases"],
					"additionalProperties": false
				}
			},
			"relations": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"src": {"type": "string"},
						"rel": {"type": "string"},
						"dst": {"type": "string"},
						"evidence": {"type": "string"}
					},
					"required": ["src", "rel", "dst", "evidence"],
					"additionalProperties": false
				}
			}
		},
		"required": ["entities", "relations"],
		"additionalProperties": false
	}`),
}

// kgDocPrefixTokenCap is the same ~8k cl100k_base budget the
// ContextualPrefix path uses. Documents larger than this get
// truncated; the chunk itself ALWAYS goes through unmodified
// because that's the actual extraction target.
const kgDocPrefixTokenCap = 8000

// ExtractKG runs the AP-C1 LLM extractor on one chunk. The full
// document body is sent as a stable prefix (auto-cached by the
// provider on subsequent chunks of the same file); the chunk goes
// last in `<chunk>...</chunk>` tags. modelOverride empty falls back
// to the resolver default — operators usually point at a small/fast
// model since extraction is structured-output, not reasoning.
//
// Output normalisation:
//   - canonical_name + alias entries are TrimSpaced; empty drops.
//   - type is lower-cased; unknown types fall back to "other"
//     (the AllowedKGEntityTypes map is the closed enum).
//   - relations whose src or dst are missing from the entity list
//     drop — the LLM occasionally emits a relation referencing an
//     entity it didn't list.
//
// On any LLM-side failure (call error, parse error) returns the
// zero KGExtraction + a non-nil error. Caller (ingestion processor)
// is expected to log and proceed with the chunk anyway — KG
// extraction is best-effort, never blocks ingestion.
func ExtractKG(ctx context.Context, resolver *ConfigResolver, documentName, documentBody, chunkContent, kbID, lang, modelOverride string) (KGExtraction, error) {
	start := time.Now()
	defer func() {
		observability.ObserveKGExtractionSeconds(time.Since(start).Seconds())
	}()

	// Truncate the document prefix to the cl100k_base cap. We use
	// splitter.CountTokens for consistency with ContextualPrefix —
	// the count is approximate but stable across providers.
	if t := splitter.CountTokens(documentBody); t > kgDocPrefixTokenCap {
		// Naive truncate: take the first N tokens worth of bytes.
		// CountTokens-based byte-precise truncation would be exact
		// but heavier; the entity extractor only needs enough
		// context to disambiguate, not the tail of the document.
		approxBytes := len(documentBody) * kgDocPrefixTokenCap / t
		if approxBytes > 0 && approxBytes < len(documentBody) {
			documentBody = documentBody[:approxBytes] + "\n[…]"
		}
	}

	sys := prompts.KGExtractorSystemPrompt(lang)
	user := prompts.KGExtractorUserPrompt(documentName, documentBody, chunkContent)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, kgExtractionSpec)
	if err != nil {
		observability.RecordKGExtraction("error")
		return KGExtraction{}, fmt.Errorf("kg_extractor: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordKGExtraction("error")
		return KGExtraction{}, fmt.Errorf("kg_extractor: empty completion")
	}

	parsed, perr := parseKGJSON(res.Content)
	if perr != nil {
		observability.RecordKGExtraction("error")
		logctx.From(ctx).Warn("kg_extractor: parse failed",
			"error", perr, "raw", firstNChars(res.Content, 240))
		return KGExtraction{}, fmt.Errorf("kg_extractor: parse: %w", perr)
	}

	allowed := prompts.AllowedKGEntityTypes()
	out := KGExtraction{}
	known := make(map[string]bool, len(parsed.Entities))
	for _, e := range parsed.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(e.Type))
		if !allowed[t] {
			t = "other"
		}
		// Dedupe alias entries; drop the canonical name from aliases
		// (it's redundant + would make the GIN index hit twice).
		aliasSet := make(map[string]bool, len(e.Aliases))
		aliases := make([]string, 0, len(e.Aliases))
		for _, a := range e.Aliases {
			a = strings.TrimSpace(a)
			if a == "" || a == name || aliasSet[a] {
				continue
			}
			aliasSet[a] = true
			aliases = append(aliases, a)
		}
		out.Entities = append(out.Entities, KGEntity{
			Name:    name,
			Type:    t,
			Aliases: aliases,
		})
		known[name] = true
	}
	for _, r := range parsed.Relations {
		src := strings.TrimSpace(r.Src)
		dst := strings.TrimSpace(r.Dst)
		rel := strings.TrimSpace(r.Rel)
		if src == "" || dst == "" || rel == "" {
			continue
		}
		if !known[src] || !known[dst] {
			// Drop relations that reference an entity the LLM
			// didn't list — typically a hallucinated cross-chunk
			// edge. Logged as a metric so we can spot a misbehaving
			// model.
			observability.RecordKGExtractionRelationDropped()
			continue
		}
		out.Relations = append(out.Relations, KGRelation{
			Src:      src,
			Rel:      rel,
			Dst:      dst,
			Evidence: strings.TrimSpace(r.Evidence),
		})
	}

	if len(out.Entities) == 0 && len(out.Relations) == 0 {
		observability.RecordKGExtraction("empty")
	} else {
		observability.RecordKGExtraction("ok")
	}
	logctx.From(ctx).Info("rag.kg_extraction",
		"stage", "kg_extraction",
		"entities", len(out.Entities),
		"relations", len(out.Relations),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

func parseKGJSON(text string) (KGExtraction, error) {
	var out KGExtraction
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return out, nil
		}
	}
	return KGExtraction{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
