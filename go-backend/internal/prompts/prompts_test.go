package prompts

import (
	"strings"
	"testing"
)

func TestSummarySystemPrompt(t *testing.T) {
	t.Run("German contains Zusammenfasser", func(t *testing.T) {
		got := SummarySystemPrompt("de")
		if !strings.Contains(got, "Zusammenfasser") {
			t.Errorf("expected German summary prompt to contain 'Zusammenfasser', got: %s", got)
		}
	})

	t.Run("English contains summarizer", func(t *testing.T) {
		got := SummarySystemPrompt("en")
		if !strings.Contains(got, "summarizer") {
			t.Errorf("expected English summary prompt to contain 'summarizer', got: %s", got)
		}
	})

	t.Run("English is default for unknown lang", func(t *testing.T) {
		got := SummarySystemPrompt("fr")
		if !strings.Contains(got, "summarizer") {
			t.Errorf("expected default prompt to contain 'summarizer', got: %s", got)
		}
	})
}

func TestSummaryUserPrompt(t *testing.T) {
	t.Run("English contains context and topic blocks", func(t *testing.T) {
		got := SummaryUserPrompt("en", "some context", "some topic")
		if !strings.Contains(got, "<context>\nsome context\n</context>") {
			t.Errorf("expected user prompt to contain context block, got: %s", got)
		}
		if !strings.Contains(got, "<topic>\nsome topic\n</topic>") {
			t.Errorf("expected user prompt to contain topic block, got: %s", got)
		}
	})

	t.Run("German contains context and topic blocks", func(t *testing.T) {
		got := SummaryUserPrompt("de", "some context", "some topic")
		if !strings.Contains(got, "<context>\nsome context\n</context>") {
			t.Errorf("expected user prompt to contain context block, got: %s", got)
		}
		if !strings.Contains(got, "<topic>\nsome topic\n</topic>") {
			t.Errorf("expected user prompt to contain topic block, got: %s", got)
		}
	})
}

func TestMathSolverSystemPrompt(t *testing.T) {
	t.Run("German contains Mathematik", func(t *testing.T) {
		got := MathSolverSystemPrompt("de")
		if !strings.Contains(got, "Mathematik") {
			t.Errorf("expected German math prompt to contain 'Mathematik', got: %s", got)
		}
	})

	t.Run("English contains math problem solver", func(t *testing.T) {
		got := MathSolverSystemPrompt("en")
		if !strings.Contains(got, "math problem solver") {
			t.Errorf("expected English math prompt to contain 'math problem solver', got: %s", got)
		}
	})

	t.Run("German contains ERROR fallback", func(t *testing.T) {
		got := MathSolverSystemPrompt("de")
		if !strings.Contains(got, "ERROR") {
			t.Errorf("expected German math prompt to contain 'ERROR', got: %s", got)
		}
	})

	t.Run("English contains ERROR fallback", func(t *testing.T) {
		got := MathSolverSystemPrompt("en")
		if !strings.Contains(got, "ERROR") {
			t.Errorf("expected English math prompt to contain 'ERROR', got: %s", got)
		}
	})
}

func TestMathSolverUserPrompt(t *testing.T) {
	got := MathSolverUserPrompt("2 plus 2")
	expected := "<problem>\n2 plus 2\n</problem>"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

// TestContextualPrefixUser_StablePrefixForCaching verifies the order critical
// to OpenAI-compatible automatic prompt caching: the document MUST appear
// before the chunk so that two calls for the same file share an identical
// byte prefix (cache key) up to where the chunk text starts.
func TestContextualPrefixUser_StablePrefixForCaching(t *testing.T) {
	doc := "FULL_DOC_TEXT"
	chunkA := "first chunk text"
	chunkB := "second different chunk text"

	a := ContextualPrefixUser("file.pdf", doc, chunkA)
	b := ContextualPrefixUser("file.pdf", doc, chunkB)

	if !strings.Contains(a, "<document name=\"file.pdf\">") {
		t.Errorf("expected document open tag with file name, got: %s", a)
	}
	if !strings.Contains(a, "<chunk>") || !strings.Contains(a, "</chunk>") {
		t.Errorf("expected chunk tags in prompt, got: %s", a)
	}

	// Document position must precede chunk position so the cacheable
	// prefix covers the whole doc.
	docIdx := strings.Index(a, doc)
	chunkIdx := strings.Index(a, chunkA)
	if docIdx < 0 || chunkIdx < 0 || docIdx >= chunkIdx {
		t.Errorf("document must appear before chunk for caching: docIdx=%d chunkIdx=%d", docIdx, chunkIdx)
	}

	// Find where the two prompts start to differ — that diff point must
	// land at-or-after the chunk-tag boundary (otherwise something
	// upstream of the chunk varies and the cache misses).
	diff := 0
	for diff < len(a) && diff < len(b) && a[diff] == b[diff] {
		diff++
	}
	chunkTagIdx := strings.Index(a, "<chunk>")
	if diff < chunkTagIdx {
		t.Errorf("prompt prefix diverges before <chunk> tag (diff=%d, chunkTagIdx=%d) — cache hits would break", diff, chunkTagIdx)
	}
}

func TestStepBackPrompt_LanguageVariants(t *testing.T) {
	en := StepBackPrompt("en")
	if en == "" {
		t.Fatal("English prompt must not be empty")
	}
	if !strings.Contains(strings.ToLower(en), "step back") && !strings.Contains(strings.ToLower(en), "broader") && !strings.Contains(strings.ToLower(en), "abstraction") {
		t.Errorf("English prompt should mention step-back/broader/abstraction; got: %s", en)
	}

	de := StepBackPrompt("de")
	if de == "" {
		t.Fatal("German prompt must not be empty")
	}
	if de == en {
		t.Errorf("German prompt should differ from English")
	}

	def := StepBackPrompt("xx")
	if def != en {
		t.Errorf("unknown language should fall back to English")
	}
}

// TestContextualPrefixSystem_RoleAwareContent guards the prompt's role-aware
// content. Phase 2 (2026-05-08) tightened this prompt to require named
// persons + their role/title in the prefix, after a diagnostic showed only
// 2 of 20 "Eberhard Kurz" chunks captured his role in the prefix. See
// docs/superpowers/specs/2026-05-08-phase2-role-aware-contextual-prefix-design.md.
func TestContextualPrefixSystem_RoleAwareContent(t *testing.T) {
	got := ContextualPrefixSystem()
	if strings.TrimSpace(got) == "" {
		t.Fatal("ContextualPrefixSystem must not be empty")
	}
	for _, want := range []string{
		"named persons",
		"role or title",
		"DO NOT invent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ContextualPrefixSystem: missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

// TestContextualPrefixSystem_StableAcrossCalls guards prompt-cache-key
// invariance: the system prompt must be byte-identical on every call,
// otherwise OpenAI/DeepSeek/vLLM provider-side prompt caching produces a
// fresh key per call and the ~90% cost reduction Anthropic's contextual
// retrieval pattern relies on disappears.
func TestContextualPrefixSystem_StableAcrossCalls(t *testing.T) {
	first := ContextualPrefixSystem()
	for i := 0; i < 5; i++ {
		if got := ContextualPrefixSystem(); got != first {
			t.Fatalf("ContextualPrefixSystem returned different content on call %d:\n--- first ---\n%s\n--- now ---\n%s", i+2, first, got)
		}
	}
}

// TestContextualPrefixUser_PreservesBodyVerbatim locks in that document and
// chunk bodies are NOT escaped. Escaping degraded the enrichment LLM's view
// of real technical content (code, URLs, quoted strings), which then
// poisoned the generated context prefix and measurably hurt retrieval.
// The filename IS escaped because it sits inside `name="..."` — a stray `"`
// would actually break the wrapper.
func TestContextualPrefixUser_PreservesBodyVerbatim(t *testing.T) {
	doc := "code: if x < 3 && y > 4 { do() } URL: https://example.com/?a=1&b=2"
	chunk := `quoted "hello" and <tag>value</tag>`

	got := ContextualPrefixUser(`name"with"quotes.pdf`, doc, chunk)

	// Body content survives unescaped — retrieval quality depends on the
	// LLM seeing the real text, not HTML entities.
	if !strings.Contains(got, "x < 3 && y > 4") {
		t.Errorf("document body was modified; expected raw inequalities, got: %s", got)
	}
	if !strings.Contains(got, "?a=1&b=2") {
		t.Errorf("document body was modified; expected raw URL ampersand, got: %s", got)
	}
	if !strings.Contains(got, `quoted "hello" and <tag>value</tag>`) {
		t.Errorf("chunk body was modified; expected raw HTML/XML, got: %s", got)
	}

	// Filename IS escaped so quotes can't close the attribute.
	if strings.Contains(got, `name="name"with"quotes.pdf"`) {
		t.Errorf("filename quotes leaked into attribute: %s", got)
	}
	if !strings.Contains(got, `name="name&quot;with&quot;quotes.pdf"`) {
		t.Errorf("filename quotes should be escaped, got: %s", got)
	}
}

func TestQueryClassifierPrompt(t *testing.T) {
	t.Run("German includes filter category and few-shot examples", func(t *testing.T) {
		got := QueryClassifierPrompt("de")
		for _, want := range []string{
			"Filterfragen",
			"Pro-Dokument-Attributprüfungen",
			`"Welche KI-bezogenen Projekte haben Status entschieden?"`,
			`"Welche Projekte gibt es im Portfolio?"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("German prompt missing %q", want)
			}
		}
	})
	t.Run("English includes filter category and few-shot examples", func(t *testing.T) {
		got := QueryClassifierPrompt("en")
		for _, want := range []string{
			"Filter queries with combined criteria",
			"per-document attribute checks",
			`"Which AI-related projects have status 'decided'?"`,
			`"What projects exist in the portfolio?"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("English prompt missing %q", want)
			}
		}
	})
	t.Run("response format constraint preserved", func(t *testing.T) {
		for _, lang := range []string{"de", "en"} {
			got := QueryClassifierPrompt(lang)
			if !strings.Contains(got, `{"isComplex": true}`) || !strings.Contains(got, `{"isComplex": false}`) {
				t.Errorf("%s: response-format JSON constraint missing", lang)
			}
		}
	})
}

func TestPlanQueriesSystemPrompt_EN_MentionsJSONSchema(t *testing.T) {
	got := PlanQueriesSystemPrompt("en")
	for _, want := range []string{"JSON", "queries", "rationale", "complex_reasoning"} {
		if !strings.Contains(got, want) {
			t.Errorf("PlanQueriesSystemPrompt(en): missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestPlanQueriesSystemPrompt_DE_UsesGerman(t *testing.T) {
	got := PlanQueriesSystemPrompt("de")
	for _, want := range []string{"JSON", "Teilfragen", "queries", "rationale"} {
		if !strings.Contains(got, want) {
			t.Errorf("PlanQueriesSystemPrompt(de): missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestPlanQueriesUserPrompt_EmbedsQuestion(t *testing.T) {
	got := PlanQueriesUserPrompt("Welche Projekte nutzen X?")
	if !strings.Contains(got, "Welche Projekte nutzen X?") {
		t.Errorf("PlanQueriesUserPrompt: question not embedded; got:\n%s", got)
	}
}

func TestIterateActionSystemPrompt_EN_CoversBothActions(t *testing.T) {
	got := IterateActionSystemPrompt("en")
	for _, want := range []string{`"action"`, `"search"`, `"answer"`, `"queries"`, `"reason"`} {
		if !strings.Contains(got, want) {
			t.Errorf("IterateActionSystemPrompt(en): missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestIterateActionUserPrompt_EmbedsQuestionAndChunks(t *testing.T) {
	got := IterateActionUserPrompt("What is X?", "[1] X is Y.", 2)
	if !strings.Contains(got, "What is X?") {
		t.Errorf("IterateActionUserPrompt: question not embedded")
	}
	if !strings.Contains(got, "[1] X is Y.") {
		t.Errorf("IterateActionUserPrompt: chunk summaries not embedded")
	}
	if !strings.Contains(got, "round 2") && !strings.Contains(got, "Round 2") {
		t.Errorf("IterateActionUserPrompt: round number not embedded; got:\n%s", got)
	}
}

func TestIterateActionSystemPrompt_DE_CoversBothActions(t *testing.T) {
	// JSON keys must remain English regardless of prompt language; the DE
	// prompt prose changes but the schema field names ("action", "queries",
	// etc.) are protocol contract with the parser and must NEVER be
	// translated. This test guards against an accidental translation.
	got := IterateActionSystemPrompt("de")
	for _, want := range []string{`"action"`, `"search"`, `"answer"`, `"queries"`, `"reason"`} {
		if !strings.Contains(got, want) {
			t.Errorf("IterateActionSystemPrompt(de): missing JSON key %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestTabularChartGuidance(t *testing.T) {
	for _, lang := range []string{"en", "de"} {
		g := TabularChartGuidance(lang)
		if g == "" {
			t.Fatalf("%s: empty guidance", lang)
		}
		for _, want := range []string{"```chart", "\"type\"", "\"config\"", "\"series\"", "bar", "line", "pie", "area", "scatter", "table_query"} {
			if !strings.Contains(g, want) {
				t.Fatalf("%s: guidance missing %q", lang, want)
			}
		}
	}
}

func TestAgenticNeedsMoreInfoSystemPrompt_FilterStrictness(t *testing.T) {
	// Pins the strict-filter clause added 2026-05-07. Without it the critique
	// returned sufficient=true for Q023 ("Welche KI-bezogenen Projekte haben
	// Status entschieden?") on the strength of template-boilerplate keyword
	// hits, stopping agentic at hop 1 and producing a shallow answer.
	t.Run("German prompt rejects keyword-only sufficiency", func(t *testing.T) {
		got := AgenticNeedsMoreInfoSystemPrompt("de")
		for _, want := range []string{
			"Streng sein",
			"Filter-/Listenfragen",
			"verifiziertem Attribut",
			"Stichworte allein",
			"Vorlagen-Boilerplate",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("German critique prompt missing %q", want)
			}
		}
	})
	t.Run("English prompt rejects keyword-only sufficiency", func(t *testing.T) {
		got := AgenticNeedsMoreInfoSystemPrompt("en")
		for _, want := range []string{
			"Be strict on filter/list questions",
			"verified",
			"keyword presence",
			"template boilerplate",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("English critique prompt missing %q", want)
			}
		}
	})
}
