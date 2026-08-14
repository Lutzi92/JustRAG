package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// configReadHelpers are the functions through which site_config keys are read.
// In all four the key is Args[2].
var configReadHelpers = map[string]bool{
	"readBool":   true,
	"readInt":    true,
	"readFloat":  true,
	"readString": true,
}

// scannedPackages are the packages that read site_config keys on the answering
// path. internal/vector is included because mmr_lambda, recency_boost_enabled,
// query_cache_enabled and chat_feedback_boost_enabled are read there, not in
// internal/chat. Most internal/vector keys are read only via route 3 (see
// collectConfigKeys) — internal/vector has zero calls to the four read
// helpers.
var scannedPackages = []string{
	filepath.Join("..", "chat"),
	filepath.Join("..", "vector"),
}

// keyShaped filters out incidental string literals (SQL fragments, labels)
// that appear in the same composite literals as config keys.
var keyShaped = regexp.MustCompile(`^[a-z][a-z0-9_]{3,}$`)

// minExpectedKeys guards against a silently broken parse. If a refactor renames
// the helpers or changes the argument order, the walk would find 0 keys and
// these tests would pass vacuously — the exact failure mode this file exists to
// prevent.
//
// Measured 2026-08-14 via collectConfigKeys() across internal/chat +
// internal/vector (all three routes, see the doc comment above): 158 distinct
// keys. Floor set ~10% below that, so the guard still fires well before a
// route silently regresses to zero but tolerates small day-to-day additions.
const minExpectedKeys = 142

// collectConfigKeys walks scannedPackages and returns every site_config key it
// can see, via three routes:
//
//  1. Calls to the four read helpers, where the key is Args[2].
//  2. String elements of []string composite literals bound to an identifier
//     whose name ends in "Keys" — e.g. cragConfigKeys in chat/service.go:329,
//     whose keys (crag_enabled, adaptive_routing_enabled, step_back_enabled)
//     are read via readSiteConfigBatch and never appear as a helper argument.
//  3. String keys of map[string]siteConfigSetter composite literals — the
//     per-key parser registry in internal/vector/search_siteconfig.go
//     (siteConfigParsers), which is the SOLE route through which ~30
//     internal/vector keys (mmr_lambda, recency_boost_enabled,
//     rerank_blend_alpha*, top_n_*, bm25_*, hybrid_dynamic_alpha_*, …) are
//     read. None of these appear as a helper argument or a "*Keys" []string
//     literal, so without route 3 TestNodeKeysAreActuallyRead falsely reports
//     "mmr_lambda"/"recency_boost_enabled" (already claimed by NodeMMR /
//     NodeRecencyBoost) as unread — this was caught live: running the walk
//     with only routes 1-2 fails exactly that way. Matched on the map's
//     VALUE type (siteConfigSetter) rather than the variable name, since
//     that is the actual, non-coincidental signal that a map is this read
//     route and not an unrelated map[string]bool/int/any literal elsewhere
//     in the same packages (there are several, e.g. chat/http_send.go's
//     supportedLanguages).
//
// Missing any route makes TestNodeKeysAreActuallyRead report false failures.
func collectConfigKeys(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}

	for _, pkgDir := range scannedPackages {
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			t.Fatalf("read dir %s: %v", pkgDir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(pkgDir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				// Route 1: helper calls.
				case *ast.CallExpr:
					ident, ok := node.Fun.(*ast.Ident)
					if !ok || !configReadHelpers[ident.Name] || len(node.Args) < 3 {
						return true
					}
					if key, ok := stringLit(node.Args[2]); ok {
						found[key] = true
					}

				// Route 2: []string{...} bound to an identifier ending in "Keys".
				case *ast.ValueSpec:
					for i, nameIdent := range node.Names {
						if i >= len(node.Values) {
							continue
						}
						comp, ok := node.Values[i].(*ast.CompositeLit)
						if !ok {
							continue
						}
						if strings.HasSuffix(nameIdent.Name, "Keys") {
							if _, ok := comp.Type.(*ast.ArrayType); ok {
								for _, elt := range comp.Elts {
									if key, ok := stringLit(elt); ok && keyShaped.MatchString(key) {
										found[key] = true
									}
								}
							}
						}
					}

				// Route 3: map[string]siteConfigSetter{...} composite literals,
				// keyed by string literal. See collectConfigKeys' doc comment.
				case *ast.CompositeLit:
					mapType, ok := node.Type.(*ast.MapType)
					if !ok {
						return true
					}
					valueIdent, ok := mapType.Value.(*ast.Ident)
					if !ok || valueIdent.Name != "siteConfigSetter" {
						return true
					}
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if key, ok := stringLit(kv.Key); ok && keyShaped.MatchString(key) {
							found[key] = true
						}
					}
				}
				return true
			})
		}
	}
	return found
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// ignoredKeys are site_config keys that are deliberately NOT drawn on the
// workflow canvas. Every entry needs a reason. Adding a new pipeline flag
// without either a NodeSpec or an entry here fails this test — which is the
// point.
var ignoredKeys = map[string]string{
	// Model/tier plumbing — a property of a node, not a node.
	"model_tier_fast":                "model selection, not a pipeline stage",
	"chat_corpus_table_model":        "model selection",
	"chat_context_compression_model": "model selection",
	"chat_drift_model":               "model selection",
	"chat_compare_model":             "model selection",
	"crag_grader_model":              "model selection",
	"kg_extraction_model":            "model selection",
	"query_decompose_model":          "model selection",
	"hype_model":                     "model selection",
	"chat_longmem_conflict_model":    "model selection",
	"chat_factuality_verifier_model": "model selection",
	"chat_plan_execute_model":        "model selection",
	"agent_team_router_model":        "model selection",
	"describe_image_model":           "model selection",

	// Ingestion-time — not part of the answering pipeline (spec: non-goals).
	"raptor_enabled":        "ingestion-time",
	"parent_child_enabled":  "ingestion-time",
	"contextual_enrichment": "ingestion-time",
	"kg_extraction_enabled": "ingestion-time",
	"embedding_batch_size":  "ingestion-time",
	"docling_enabled":       "ingestion-time",
	"hype_enabled":          "ingestion-time",
	"late_chunking_enabled": "ingestion-time",

	// Operational / security — deliberately never per-KB.
	"agents_allow_privileged_tools": "security gate, system-admin only",
	"langfuse_base_url":             "observability config",
	"chat_turn_budget_seconds":      "operational budget",

	// Observability / sampling — no user-visible pipeline stage.
	"ragas_sampling_enabled": "background eval sampling",
	"ragas_sampling_rate":    "tuning knob for the out-of-scope background eval sampling",

	// --- Resolved during Task 4's first coverage run ---

	// LLM sampling parameter for the always-on Answer node — same category
	// as the model-selection keys above (a property of the LLM call, not a
	// pipeline-stage toggle).
	"chat_answer_temperature": "LLM sampling parameter, same category as model selection",
	"chat_refine_model":       "model selection",

	// Ingestion-time — siblings of the already-ignored raptor_enabled /
	// parent_child_enabled / hype_enabled (whole ingestion subsystems).
	"raptor_branching_factor":     "ingestion-time, tuning knob for the ingestion-time raptor_enabled",
	"raptor_clustering_algorithm": "ingestion-time, tuning knob for the ingestion-time raptor_enabled",
	"raptor_leiden_resolution":    "ingestion-time, tuning knob for the ingestion-time raptor_enabled",
	"raptor_max_levels":           "ingestion-time, tuning knob for the ingestion-time raptor_enabled",
	"raptor_min_chunks":           "ingestion-time, tuning knob for the ingestion-time raptor_enabled",
	"parent_chunk_size":           "ingestion-time, tuning knob for the ingestion-time parent_child_enabled",
	"child_chunk_size":            "ingestion-time, tuning knob for the ingestion-time parent_child_enabled",
	"hype_questions_per_chunk":    "ingestion-time (question generation at ingest), tuning knob for the ingestion-time hype_enabled",

	// Whole answer-time subsystems that Task 3's node vocabulary does not
	// model as a pipeline stage. Each is a real, documented feature (see
	// CLAUDE.md) but sits outside the standard retrieve→rerank→answer→verify
	// path this diagram draws; modeling any of them as a node/edge is a
	// separate design decision for a later phase, not a byproduct of
	// writing this coverage guard.
	"chat_longmem_enabled":             "long-term per-user memory subsystem, unrepresented in the phase-0/1 node vocabulary",
	"chat_longmem_recall_semantic":     "tuning knob for the out-of-scope long-term memory subsystem",
	"chat_longmem_recall_top_k":        "tuning knob for the out-of-scope long-term memory subsystem",
	"chat_longmem_min_salience":        "tuning knob for the out-of-scope long-term memory subsystem",
	"chat_longmem_decay_days":          "tuning knob for the out-of-scope long-term memory subsystem",
	"chat_longmem_conflict_resolution": "tuning knob for the out-of-scope long-term memory subsystem",
	"chat_longmem_conflict_candidates": "tuning knob for the out-of-scope long-term memory subsystem",

	"chat_session_memory_enabled": "session-scoped chat memory subsystem, unrepresented in the phase-0/1 node vocabulary",

	"chat_longcontext_enabled":    "System-2 long-context routing for global-synthesis queries, unrepresented in the phase-0/1 node vocabulary",
	"chat_longcontext_max_tokens": "tuning knob for the out-of-scope long-context routing subsystem",

	"chat_community_search_enabled": "community-primed global search variant, unrepresented in the phase-0/1 node vocabulary",
	"chat_community_search_top_k":   "tuning knob for the out-of-scope community-search variant",

	"chat_compare_enabled":              "in-chat document comparison is a separate answer mode (compares an uploaded file against the KB), not a step of the standard retrieval/answer pipeline",
	"chat_compare_max_sections":         "tuning knob for the out-of-scope in-chat comparison mode",
	"chat_compare_concurrency":          "tuning knob for the out-of-scope in-chat comparison mode",
	"chat_compare_peers_per_section":    "tuning knob for the out-of-scope in-chat comparison mode",
	"chat_compare_attachment_ttl_hours": "tuning knob for the out-of-scope in-chat comparison mode",
	"chat_compare_max_file_bytes":       "tuning knob for the out-of-scope in-chat comparison mode",

	"chat_tabular_query_enabled":  "structured spreadsheet Q&A is a separate answer path with its own tool dispatch, not the retrieval/answer pipeline modeled here",
	"chat_tabular_charts_enabled": "tuning knob for the out-of-scope tabular Q&A path",

	"multipass_extraction_enabled": "answer-time per-chunk extraction pre-pass (lost-in-the-middle mitigation); a real pipeline stage but unrepresented in the phase-0/1 node vocabulary — candidate for a future node, not added here to avoid an unreviewed topology change",
	"multipass_min_chunks":         "tuning knob for the out-of-scope multipass-extraction pre-pass",

	// Date-aware chat: prompt injection + MCP tool + a deterministic listing
	// path, not steps of the standard retrieval/answer pipeline.
	"chat_date_awareness_enabled":             "prompt-injection feature (current date into the system prompt), not a retrieval/answer pipeline stage",
	"chat_date_timezone":                      "tuning knob for the out-of-scope date-awareness feature",
	"chat_date_tools_enabled":                 "gates an MCP tool (recent_documents), not a pipeline stage",
	"chat_date_tools_max_results":             "tuning knob for the out-of-scope date-tools MCP feature",
	"chat_recency_listing_enabled":            "deterministic recency-listing path for 'what's new' queries — a distinct mechanism from the RecencyBoost retrieval-time decay node (window-scopes retrieval + injects a file listing instead of ranking); unrepresented in the phase-0/1 node vocabulary",
	"chat_recency_listing_window_days":        "tuning knob for the out-of-scope recency-listing feature",
	"chat_recency_listing_max_results":        "tuning knob for the out-of-scope recency-listing feature",
	"chat_recency_listing_name_match_enabled": "tuning knob for the out-of-scope recency-listing feature",

	// Answer-time correctness fixes / kill switches (CLAUDE.md: "correctness
	// fixes; the keys are kill switches") — not a design trade-off a KB
	// admin tunes the way they tune CRAG/MMR/reranking, so not modeled as a
	// pipeline-stage toggle.
	"chat_answer_history_enabled":     "answer-time correctness fix (conversation history for the answer LLM), default-on kill switch, not a togglable pipeline stage",
	"chat_answer_history_messages":    "tuning knob for the answer-time conversation-history correctness fix",
	"chat_answer_history_max_chars":   "tuning knob for the answer-time conversation-history correctness fix",
	"chat_transform_followup_enabled": "retrieval-free reformat follow-ups, default-on correctness/UX kill switch, not a retrieval/answer pipeline stage",

	// Operational budget siblings of the already-ignored chat_turn_budget_seconds.
	"chat_turn_budget_tokens":     "operational budget, sibling of chat_turn_budget_seconds",
	"chat_turn_budget_tool_calls": "operational budget, sibling of chat_turn_budget_seconds",

	// Internal MCP-dispatch plumbing: which code path an orchestrator's
	// search action goes through (direct vs. MCP registry), not a visible
	// pipeline-behavior change for a KB admin.
	"chat_use_mcp_tools": "internal MCP-dispatch plumbing toggle for orchestrator search calls; both paths perform the same search, not a visible pipeline behavior change",

	// Utility endpoint unrelated to the answering pipeline, sibling of the
	// already-ignored describe_image_model.
	"describe_image_enabled": "utility endpoint (POST /api/describe-image), not part of the answering pipeline",
}

// TestEveryPipelineFlagIsDrawnOrIgnored is the anti-drift guard (spec §4.4).
func TestEveryPipelineFlagIsDrawnOrIgnored(t *testing.T) {
	claimed := map[string]NodeID{}
	for _, n := range Nodes() {
		for _, k := range n.Keys {
			claimed[k] = n.ID
		}
	}

	found := collectConfigKeys(t)

	// Floor check FIRST: a broken parse must fail loudly, not pass vacuously.
	if len(found) < minExpectedKeys {
		t.Fatalf("AST walk found only %d config keys (expected >= %d) — the parse "+
			"is probably broken. Check that %v still read keys via %v with the key "+
			"as the third argument, and that batch key slices still end in \"Keys\".",
			len(found), minExpectedKeys, scannedPackages, configReadHelpers)
	}

	for key := range found {
		if _, drawn := claimed[key]; drawn {
			continue
		}
		if _, ignored := ignoredKeys[key]; ignored {
			continue
		}
		t.Errorf("site_config key %q is read by internal/chat but is neither "+
			"claimed by a NodeSpec.Keys nor listed in ignoredKeys. Either add it "+
			"to a node in nodes.go or add it to ignoredKeys with a reason.", key)
	}
}

// A key claimed by a node must actually be read somewhere, or the node
// advertises a control that does nothing.
func TestNodeKeysAreActuallyRead(t *testing.T) {
	found := collectConfigKeys(t)

	if len(found) < minExpectedKeys {
		t.Fatalf("AST walk found only %d config keys (expected >= %d) — parse broken",
			len(found), minExpectedKeys)
	}

	// No exemption list. If a node claims a key the walk cannot see, either the
	// key does not exist (a typo — this is how the phantom "mmr_enabled" was
	// caught) or collectConfigKeys needs to learn a new read route. Both are
	// bugs to fix, not to exempt.
	for _, n := range Nodes() {
		for _, k := range n.Keys {
			if found[k] {
				continue
			}
			t.Errorf("node %q claims key %q, but no code in %v reads it — either "+
				"the key is a typo, or collectConfigKeys is missing a read route",
				n.ID, k, scannedPackages)
		}
	}
}
