package prompts

import (
	"strings"
	"testing"
)

// TestKGExtractorSystemPrompt_ListsAllowedTypes pins the closed
// type enum into the prompt. If the prompt and AllowedKGEntityTypes
// drift apart, the storage path silently coerces unknown types to
// "other" — at scale that would warp the type distribution. Test
// keeps both lists in sync.
func TestKGExtractorSystemPrompt_ListsAllowedTypes(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"de", "en"} {
		prompt := KGExtractorSystemPrompt(lang)
		for typ := range AllowedKGEntityTypes() {
			if !strings.Contains(prompt, typ) {
				t.Errorf("lang=%s: prompt missing entity type %q", lang, typ)
			}
		}
	}
}

// TestKGExtractorUserPrompt_DocumentBeforeChunk: provider auto-cache
// hits depend on the document body being a STABLE prefix. If the
// chunk slot moved before the document, every chunk would invalidate
// the cache and the plan's 90%-cost-reduction claim disappears.
func TestKGExtractorUserPrompt_DocumentBeforeChunk(t *testing.T) {
	t.Parallel()
	got := KGExtractorUserPrompt("Müller Vertrag.pdf", "VOLLER DOCUMENT BODY", "ZIELCHUNK")
	docIdx := strings.Index(got, "VOLLER DOCUMENT BODY")
	chunkIdx := strings.Index(got, "ZIELCHUNK")
	if docIdx < 0 || chunkIdx < 0 {
		t.Fatalf("payload missing one of the parts: %s", got)
	}
	if docIdx > chunkIdx {
		t.Errorf("document body must come BEFORE the chunk for cache hits; got doc@%d chunk@%d", docIdx, chunkIdx)
	}
	if !strings.Contains(got, "<chunk>") || !strings.Contains(got, "</chunk>") {
		t.Errorf("chunk tags missing: %s", got)
	}
}

// TestKGExtractorUserPrompt_FilenameEscaped: filenames go inside
// name="..." attributes. A stray quote in the name (rare but
// possible) would break the wrapper and confuse the extractor.
// Pin the escape behaviour.
func TestKGExtractorUserPrompt_FilenameEscaped(t *testing.T) {
	t.Parallel()
	got := KGExtractorUserPrompt(`weird"name.pdf`, "body", "chunk")
	// html.EscapeString turns " into &#34;
	if strings.Contains(got, `name="weird"name.pdf"`) {
		t.Errorf("filename quote not escaped: %q", got[:60])
	}
	if !strings.Contains(got, "&#34;") {
		t.Errorf("expected HTML-escape of quote in filename, got: %q", got[:60])
	}
}

// TestAllowedKGEntityTypes_HasOther: "other" is the safety fallback
// for the storage normalisation path. Removing it would trip the
// extractor on any type the LLM hallucinates.
func TestAllowedKGEntityTypes_HasOther(t *testing.T) {
	t.Parallel()
	if !AllowedKGEntityTypes()["other"] {
		t.Error("'other' must remain in AllowedKGEntityTypes — it's the storage normaliser fallback")
	}
}
