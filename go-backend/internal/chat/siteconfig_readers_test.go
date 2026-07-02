package chat

// Site-config reader unit tests, split out from service_test.go so each file
// stays focused on a single concern. The shared fakes (fakeSiteConfigReader,
// strPtr) live in service_test.go; Go's test compilation links every _test.go
// in the package together, so we don't import them — they're in scope.

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// FactcheckEnabled tests
// ---------------------------------------------------------------------------

func TestFactcheckEnabled_DefaultOn(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if !FactcheckEnabled(context.Background(), reader) {
		t.Error("expected true when key is missing")
	}
}

func TestFactcheckEnabled_ExplicitFalse(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"factcheck_in_chat": strPtr("false"),
	}}
	if FactcheckEnabled(context.Background(), reader) {
		t.Error("expected false when value is \"false\"")
	}
}

func TestFactcheckEnabled_ExplicitZero(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"factcheck_in_chat": strPtr("0"),
	}}
	if FactcheckEnabled(context.Background(), reader) {
		t.Error("expected false when value is \"0\"")
	}
}

func TestFactcheckEnabled_NilReader(t *testing.T) {
	t.Parallel()
	if !FactcheckEnabled(context.Background(), nil) {
		t.Error("expected true when reader is nil")
	}
}

// Compile-time field assertions: if these declarations stop compiling, the
// expected fields have been renamed or retyped. No runtime cost.
var (
	_ = ChatContext{}.FinalChunks                   // []vector.SearchChunk
	_ = ChatContextParams{}.ForceEnumerationPrepass // *bool
)

// ---------------------------------------------------------------------------
// AP-A1 factuality refine gate config readers
// ---------------------------------------------------------------------------

func TestChatFactualityGateEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatFactualityGateEnabled(context.Background(), r) {
		t.Error("default must be off")
	}
	if ChatFactualityGateEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
}

func TestChatFactualityGateEnabled_AcceptsTrueAndOne(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"true", "TRUE", "1", "  true  "} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_factuality_gate_enabled": strPtr(v),
		}}
		if !ChatFactualityGateEnabled(context.Background(), r) {
			t.Errorf("value %q must enable gate", v)
		}
	}
}

func TestChatFactualityGateMaxRefines_DefaultAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 1},
		{"0", 0},
		{"1", 1},
		{"2", 2},
		{"3", 1},   // out of range → default
		{"-1", 1},  // out of range → default
		{"abc", 1}, // unparsable → default
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_factuality_gate_max_refines"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatFactualityGateMaxRefines(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

func TestChatRefineModel_DefaultEmptyAndTrim(t *testing.T) {
	t.Parallel()
	if got := ChatRefineModel(context.Background(), nil); got != "" {
		t.Errorf("nil reader: got %q, want empty", got)
	}
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_refine_model": strPtr("  qwen3-vl  "),
	}}
	if got := ChatRefineModel(context.Background(), r); got != "qwen3-vl" {
		t.Errorf("got %q, want qwen3-vl (trimmed)", got)
	}
}

// ---------------------------------------------------------------------------
// AP-A3 turn-budget config readers
// ---------------------------------------------------------------------------

func TestChatTurnBudgetSeconds_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 0},      // unset → unlimited (default)
		{"0", 0},     // explicit 0 → unlimited
		{"60", 60},   // valid in range
		{"600", 600}, // upper bound
		{"601", 0},   // out of range → default
		{"-1", 0},    // negative → default
		{"abc", 0},   // unparsable → default
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_turn_budget_seconds"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatTurnBudgetSeconds(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

func TestChatTurnBudgetTokens_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 0},
		{"0", 0},
		{"50000", 50000},
		{"2000000", 2000000}, // upper bound
		{"2000001", 0},       // over bound → default
		{"-100", 0},
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_turn_budget_tokens"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatTurnBudgetTokens(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// AP-B2 code_exec gate
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// AP-D2 self-rag config readers
// ---------------------------------------------------------------------------

func TestChatSelfRAGEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatSelfRAGEnabled(context.Background(), r) {
		t.Error("default must be off — Self-RAG opts in deliberately, replaces factuality verifier")
	}
	if ChatSelfRAGEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_self_rag_enabled"] = strPtr("true")
	if !ChatSelfRAGEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

func TestChatSelfRAGModel_TrimAndDefault(t *testing.T) {
	t.Parallel()
	if got := ChatSelfRAGModel(context.Background(), nil); got != "" {
		t.Errorf("nil reader: got %q, want empty", got)
	}
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_self_rag_model": strPtr("  gemma-4-12b  "),
	}}
	if got := ChatSelfRAGModel(context.Background(), r); got != "gemma-4-12b" {
		t.Errorf("got %q, want trimmed value", got)
	}
}

// ---------------------------------------------------------------------------
// AP-D1 longmem config readers
// ---------------------------------------------------------------------------

func TestChatLongmemEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatLongmemEnabled(context.Background(), r) {
		t.Error("default must be off — DSGVO UI is a hard prerequisite")
	}
	if ChatLongmemEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_longmem_enabled"] = strPtr("true")
	if !ChatLongmemEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

func TestChatLongmemMinSalience_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want float64
	}{
		{"", 0.5},
		{"0.0", 0.0},
		{"0.7", 0.7},
		{"1.0", 1.0},
		{"-0.1", 0.5},
		{"1.5", 0.5},
		{"abc", 0.5},
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_longmem_min_salience"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatLongmemMinSalience(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestChatLongmemRecallTopK_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 5},
		{"3", 3},
		{"20", 20},
		{"21", 5},
		{"0", 5},
		{"-1", 5},
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_longmem_recall_top_k"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatLongmemRecallTopK(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

func TestChatLongmemDecayDays_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 30},
		{"1", 1},
		{"7", 7},
		{"365", 365},
		{"366", 30},
		{"0", 30},
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_longmem_decay_days"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatLongmemDecayDays(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

func TestChatGraphRoutingEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatGraphRoutingEnabled(context.Background(), r) {
		t.Error("default must be off — graph routing requires AP-C1 KG ingestion run first")
	}
	if ChatGraphRoutingEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_graph_routing_enabled"] = strPtr("true")
	if !ChatGraphRoutingEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

// ---------------------------------------------------------------------------
// Model tier fallback (P7: model_tier_fast)
// ---------------------------------------------------------------------------

func TestChatModelTierFast_DefaultEmpty(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ChatModelTierFast(context.Background(), r); got != "" {
		t.Errorf("default must be empty (no tier configured), got %q", got)
	}
	if got := ChatModelTierFast(context.Background(), nil); got != "" {
		t.Errorf("nil reader must yield empty, got %q", got)
	}
	r.values["model_tier_fast"] = strPtr("  gemma-4-4b-it  ")
	if got := ChatModelTierFast(context.Background(), r); got != "gemma-4-4b-it" {
		t.Errorf("explicit value must be returned trimmed, got %q", got)
	}
}

// TestResolveFastTierModel_Chain pins the three-tier resolution
// chain: per-task override > model_tier_fast > empty.
func TestResolveFastTierModel_Chain(t *testing.T) {
	t.Parallel()

	const perTaskKey = "crag_grader_model"

	cases := []struct {
		name    string
		perTask *string
		tier    *string
		want    string
	}{
		{name: "neither set → empty", perTask: nil, tier: nil, want: ""},
		{name: "tier only → tier", perTask: nil, tier: strPtr("gemma-4b"), want: "gemma-4b"},
		{name: "per-task only → per-task", perTask: strPtr("gpt-4o-mini"), tier: nil, want: "gpt-4o-mini"},
		{name: "both set → per-task wins", perTask: strPtr("gpt-4o-mini"), tier: strPtr("gemma-4b"), want: "gpt-4o-mini"},
		{name: "per-task whitespace → falls through to tier", perTask: strPtr("   "), tier: strPtr("gemma-4b"), want: "gemma-4b"},
		{name: "tier whitespace → empty", perTask: nil, tier: strPtr("   "), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			values := map[string]*string{}
			if tc.perTask != nil {
				values[perTaskKey] = tc.perTask
			}
			if tc.tier != nil {
				values["model_tier_fast"] = tc.tier
			}
			r := &fakeSiteConfigReader{values: values}
			got := ResolveFastTierModel(context.Background(), r, perTaskKey)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveFastTierModel_NilReader: defensive — must not panic.
func TestResolveFastTierModel_NilReader(t *testing.T) {
	t.Parallel()
	if got := ResolveFastTierModel(context.Background(), nil, "crag_grader_model"); got != "" {
		t.Errorf("nil reader must yield empty, got %q", got)
	}
}

// TestEnrichmentModel_TierFallback: existing reader's behaviour
// with the new tier fallback. When the per-task key isn't set but
// model_tier_fast is, the reader returns the tier value. Pins
// backwards-compat: deployments using only the per-task key
// continue to work; deployments using only the tier key get the
// tier value.
func TestEnrichmentModel_TierFallback(t *testing.T) {
	t.Parallel()

	// per-task only
	r := &fakeSiteConfigReader{values: map[string]*string{
		"contextual_enrichment_model": strPtr("explicit-model"),
	}}
	if got := EnrichmentModel(context.Background(), r); got != "explicit-model" {
		t.Errorf("per-task override should win, got %q", got)
	}

	// tier only
	r = &fakeSiteConfigReader{values: map[string]*string{
		"model_tier_fast": strPtr("tier-model"),
	}}
	if got := EnrichmentModel(context.Background(), r); got != "tier-model" {
		t.Errorf("tier fallback should apply, got %q", got)
	}

	// neither
	r = &fakeSiteConfigReader{values: map[string]*string{}}
	if got := EnrichmentModel(context.Background(), r); got != "" {
		t.Errorf("neither set should yield empty (caller falls back to KB default), got %q", got)
	}
}

func TestChatGraphRoutingInjectChunks_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatGraphRoutingInjectChunks(context.Background(), r) {
		t.Error("default must be off — chunk injection is a separate opt-in from the diagnostic gate")
	}
	if ChatGraphRoutingInjectChunks(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_graph_routing_inject_chunks"] = strPtr("true")
	if !ChatGraphRoutingInjectChunks(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

func TestChatGraphRoutingMaxChunks_DefaultAndRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 15},    // unset → default
		{"abc", 15}, // unparseable → default
		{"0", 15},   // below min → default (readInt convention)
		{"1", 1},    // min
		{"15", 15},  // default value via explicit set
		{"50", 50},  // max
		{"100", 15}, // above max → default (readInt convention)
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_graph_routing_max_chunks"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatGraphRoutingMaxChunks(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

func TestChatPlanExecuteToolAware_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatPlanExecuteToolAware(context.Background(), r) {
		t.Error("default must be off — tool-aware planner is opt-in")
	}
	if ChatPlanExecuteToolAware(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_plan_execute_tool_aware"] = strPtr("true")
	if !ChatPlanExecuteToolAware(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

func TestChatCodeExecEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatCodeExecEnabled(context.Background(), r) {
		t.Error("default must be off — code_exec is opt-in per KB")
	}
	if ChatCodeExecEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_code_exec_enabled"] = strPtr("true")
	if !ChatCodeExecEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
	r.values["chat_code_exec_enabled"] = strPtr("false")
	if ChatCodeExecEnabled(context.Background(), r) {
		t.Error("explicit false must disable")
	}
}

func TestChatTabularQueryEnabled(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatTabularQueryEnabled(context.Background(), r) {
		t.Error("default must be off")
	}
	if ChatTabularQueryEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_tabular_query_enabled"] = strPtr("true")
	if !ChatTabularQueryEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
	r.values["chat_tabular_query_enabled"] = strPtr("false")
	if ChatTabularQueryEnabled(context.Background(), r) {
		t.Error("explicit false must disable")
	}
}

func TestChatTabularChartsEnabled(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatTabularChartsEnabled(context.Background(), r) {
		t.Error("default must be off")
	}
	if ChatTabularChartsEnabled(context.Background(), nil) {
		t.Error("nil reader must yield off")
	}
	r.values["chat_tabular_charts_enabled"] = strPtr("true")
	if !ChatTabularChartsEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
	r.values["chat_tabular_charts_enabled"] = strPtr("false")
	if ChatTabularChartsEnabled(context.Background(), r) {
		t.Error("explicit false must disable")
	}
}

// ---------------------------------------------------------------------------
// AP-A4 KB router config readers
// ---------------------------------------------------------------------------

func TestChatKBRouterEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatKBRouterEnabled(context.Background(), r) {
		t.Error("default must be off")
	}
	r.values["chat_kb_router_enabled"] = strPtr("true")
	if !ChatKBRouterEnabled(context.Background(), r) {
		t.Error("explicit true must enable")
	}
}

func TestChatKBRouterMinConfidence_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want float64
	}{
		{"", 0.6},     // unset → default
		{"0.3", 0.3},  // valid
		{"0.0", 0.0},  // lower bound
		{"1.0", 1.0},  // upper bound
		{"1.5", 0.6},  // out of range → default
		{"-0.1", 0.6}, // out of range → default
		{"abc", 0.6},  // unparsable → default
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_kb_router_min_confidence"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatKBRouterMinConfidence(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestChatTurnBudgetToolCalls_DefaultsAndClamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 0},
		{"5", 5},
		{"100", 100},
		{"101", 0},
		{"-1", 0},
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_turn_budget_tool_calls"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatTurnBudgetToolCalls(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// CitationValidationSemanticThreshold tests
// ---------------------------------------------------------------------------

func TestCitationValidationSemanticThreshold_DefaultWhenUnset(t *testing.T) {
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	got := CitationValidationSemanticThreshold(context.Background(), reader)
	if got != 0.85 {
		t.Errorf("unset: got %v, want 0.85", got)
	}
}

func TestCitationValidationSemanticThreshold_ParsesValid(t *testing.T) {
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"citation_validation_semantic_threshold": strPtr("0.7"),
	}}
	got := CitationValidationSemanticThreshold(context.Background(), reader)
	if got != 0.7 {
		t.Errorf("0.7 set: got %v, want 0.7", got)
	}
}

func TestCitationValidationSemanticThreshold_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"-0.1", "1.5", "abc", ""} {
		reader := &fakeSiteConfigReader{values: map[string]*string{
			"citation_validation_semantic_threshold": strPtr(raw),
		}}
		got := CitationValidationSemanticThreshold(context.Background(), reader)
		if got != 0.85 {
			t.Errorf("invalid value %q: got %v, want default 0.85", raw, got)
		}
	}
}

func TestCitationValidationSemanticThreshold_NilReader(t *testing.T) {
	got := CitationValidationSemanticThreshold(context.Background(), nil)
	if got != 0.85 {
		t.Errorf("nil reader: got %v, want default 0.85", got)
	}
}

func TestCitationValidationEnabled_DefaultOnWhenUnset(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if !CitationValidationEnabled(context.Background(), r) {
		t.Error("missing key should default to true after Phase 2 §B")
	}
}

func TestCitationValidationEnabled_ExplicitFalseStaysOff(t *testing.T) {
	for _, raw := range []string{"false", "FALSE", "0"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"citation_validation_enabled": strPtr(raw),
		}}
		if CitationValidationEnabled(context.Background(), r) {
			t.Errorf("explicit %q should keep validation off", raw)
		}
	}
}

func TestCitationValidationEnabled_ExplicitTrueStaysOn(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"citation_validation_enabled": strPtr(raw),
		}}
		if !CitationValidationEnabled(context.Background(), r) {
			t.Errorf("explicit %q should keep validation on", raw)
		}
	}
}

func TestCitationValidationEnabled_NilReaderDefaultsOn(t *testing.T) {
	if !CitationValidationEnabled(context.Background(), nil) {
		t.Error("nil reader should default to on after Phase 2 §B")
	}
}

func TestCitationValidationEnabled_GarbageValueDefaultsOn(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"citation_validation_enabled": strPtr("yes please"),
	}}
	if !CitationValidationEnabled(context.Background(), r) {
		t.Error("garbage value should fall through to the new default (on)")
	}
}

func TestAdaptiveRoutingEnabled_DefaultOffWhenUnset(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if AdaptiveRoutingEnabled(context.Background(), r) {
		t.Error("missing key should default to false (no behavior change for existing deployments)")
	}
}

func TestAdaptiveRoutingEnabled_ExplicitTrue(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"adaptive_routing_enabled": strPtr(raw),
		}}
		if !AdaptiveRoutingEnabled(context.Background(), r) {
			t.Errorf("explicit %q should enable adaptive routing", raw)
		}
	}
}

func TestAdaptiveRoutingEnabled_ExplicitFalse(t *testing.T) {
	for _, raw := range []string{"false", "FALSE", "0", "anything else"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"adaptive_routing_enabled": strPtr(raw),
		}}
		if AdaptiveRoutingEnabled(context.Background(), r) {
			t.Errorf("non-truthy %q should keep adaptive routing off", raw)
		}
	}
}

func TestAdaptiveRoutingEnabled_NilReader(t *testing.T) {
	if AdaptiveRoutingEnabled(context.Background(), nil) {
		t.Error("nil reader should default to false")
	}
}

func TestRAGASSamplingEnabled_DefaultOff(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if RAGASSamplingEnabled(context.Background(), r) {
		t.Error("missing key should default to false")
	}
}

func TestRAGASSamplingEnabled_ExplicitTrue(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_sampling_enabled": strPtr(raw),
		}}
		if !RAGASSamplingEnabled(context.Background(), r) {
			t.Errorf("explicit %q should enable sampling", raw)
		}
	}
}

func TestRAGASSamplingEnabled_ExplicitFalse(t *testing.T) {
	for _, raw := range []string{"false", "FALSE", "0", "garbage"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_sampling_enabled": strPtr(raw),
		}}
		if RAGASSamplingEnabled(context.Background(), r) {
			t.Errorf("non-truthy %q should keep sampling off", raw)
		}
	}
}

func TestRAGASSamplingEnabled_NilReader(t *testing.T) {
	if RAGASSamplingEnabled(context.Background(), nil) {
		t.Error("nil reader should default to false")
	}
}

func TestRAGASSamplingRate_DefaultZero(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := RAGASSamplingRate(context.Background(), r); got != 0.0 {
		t.Errorf("missing key: got %v, want 0.0", got)
	}
}

func TestRAGASSamplingRate_ParsesValid(t *testing.T) {
	for raw, want := range map[string]float64{
		"0.0":  0.0,
		"0.01": 0.01,
		"0.5":  0.5,
		"1.0":  1.0,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_sampling_rate": strPtr(raw),
		}}
		if got := RAGASSamplingRate(context.Background(), r); got != want {
			t.Errorf("raw=%q: got %v, want %v", raw, got, want)
		}
	}
}

func TestRAGASSamplingRate_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"-0.1", "1.5", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_sampling_rate": strPtr(raw),
		}}
		if got := RAGASSamplingRate(context.Background(), r); got != 0.0 {
			t.Errorf("invalid value %q: got %v, want default 0.0", raw, got)
		}
	}
}

func TestRAGASSamplingRate_NilReader(t *testing.T) {
	if got := RAGASSamplingRate(context.Background(), nil); got != 0.0 {
		t.Errorf("nil reader: got %v, want 0.0", got)
	}
}

func TestChatAgenticEnabled_DefaultOff(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatAgenticEnabled(context.Background(), r) {
		t.Error("missing key should default to false")
	}
}

func TestChatAgenticEnabled_ExplicitTrue(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_agentic_enabled": strPtr(raw),
		}}
		if !ChatAgenticEnabled(context.Background(), r) {
			t.Errorf("explicit %q should enable agentic chat", raw)
		}
	}
}

func TestChatAgenticEnabled_ExplicitFalse(t *testing.T) {
	for _, raw := range []string{"false", "0", "garbage"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_agentic_enabled": strPtr(raw),
		}}
		if ChatAgenticEnabled(context.Background(), r) {
			t.Errorf("non-truthy %q should keep agentic chat off", raw)
		}
	}
}

func TestChatAgenticEnabled_NilReader(t *testing.T) {
	if ChatAgenticEnabled(context.Background(), nil) {
		t.Error("nil reader should default to false")
	}
}

func TestChatAgenticMaxHops_DefaultThree(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ChatAgenticMaxHops(context.Background(), r); got != 3 {
		t.Errorf("missing key: got %v, want 3", got)
	}
}

func TestChatAgenticMaxHops_ParsesValid(t *testing.T) {
	for raw, want := range map[string]int{
		"1": 1,
		"2": 2,
		"3": 3,
		"4": 4,
		"5": 5,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_agentic_max_hops": strPtr(raw),
		}}
		if got := ChatAgenticMaxHops(context.Background(), r); got != want {
			t.Errorf("raw=%q: got %v, want %v", raw, got, want)
		}
	}
}

func TestChatAgenticMaxHops_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"0", "-1", "6", "100", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_agentic_max_hops": strPtr(raw),
		}}
		if got := ChatAgenticMaxHops(context.Background(), r); got != 3 {
			t.Errorf("invalid value %q: got %v, want default 3", raw, got)
		}
	}
}

func TestChatAgenticMaxHops_NilReader(t *testing.T) {
	if got := ChatAgenticMaxHops(context.Background(), nil); got != 3 {
		t.Errorf("nil reader: got %v, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// Phase 3 §D — parent-child chunking site-config readers
// ---------------------------------------------------------------------------

func TestParentChildEnabled_DefaultOff(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ParentChildEnabled(context.Background(), r) {
		t.Error("missing key should default to false")
	}
}

func TestParentChildEnabled_ExplicitTrue(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"parent_child_enabled": strPtr(raw),
		}}
		if !ParentChildEnabled(context.Background(), r) {
			t.Errorf("explicit %q should enable parent-child chunking", raw)
		}
	}
}

func TestParentChildEnabled_ExplicitFalse(t *testing.T) {
	for _, raw := range []string{"false", "0", "garbage"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"parent_child_enabled": strPtr(raw),
		}}
		if ParentChildEnabled(context.Background(), r) {
			t.Errorf("non-truthy %q should keep parent-child chunking off", raw)
		}
	}
}

func TestParentChildEnabled_NilReader(t *testing.T) {
	if ParentChildEnabled(context.Background(), nil) {
		t.Error("nil reader should default to false")
	}
}

func TestParentChildParentChunkSize_DefaultFiveTwelve(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ParentChildParentChunkSize(context.Background(), r); got != 512 {
		t.Errorf("missing key: got %v, want 512", got)
	}
}

func TestParentChildParentChunkSize_ParsesValid(t *testing.T) {
	for raw, want := range map[string]int{
		"256":  256,
		"512":  512,
		"768":  768,
		"1024": 1024,
		"2048": 2048,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"parent_chunk_size": strPtr(raw),
		}}
		if got := ParentChildParentChunkSize(context.Background(), r); got != want {
			t.Errorf("raw=%q: got %v, want %v", raw, got, want)
		}
	}
}

func TestParentChildParentChunkSize_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"0", "-1", "127", "4097", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"parent_chunk_size": strPtr(raw),
		}}
		if got := ParentChildParentChunkSize(context.Background(), r); got != 512 {
			t.Errorf("invalid value %q: got %v, want default 512", raw, got)
		}
	}
}

func TestParentChildChildChunkSize_DefaultOneTwentyEight(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ParentChildChildChunkSize(context.Background(), r); got != 128 {
		t.Errorf("missing key: got %v, want 128", got)
	}
}

func TestParentChildChildChunkSize_ParsesValid(t *testing.T) {
	for raw, want := range map[string]int{
		"64":  64,
		"128": 128,
		"256": 256,
		"384": 384,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"child_chunk_size": strPtr(raw),
		}}
		if got := ParentChildChildChunkSize(context.Background(), r); got != want {
			t.Errorf("raw=%q: got %v, want %v", raw, got, want)
		}
	}
}

func TestParentChildChildChunkSize_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"0", "-1", "31", "513", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"child_chunk_size": strPtr(raw),
		}}
		if got := ParentChildChildChunkSize(context.Background(), r); got != 128 {
			t.Errorf("invalid value %q: got %v, want default 128", raw, got)
		}
	}
}

// ---------------------------------------------------------------------------
// ChatPlanExecute* tests
// ---------------------------------------------------------------------------

func TestChatPlanExecuteEnabled_DefaultOff(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatPlanExecuteEnabled(context.Background(), r) {
		t.Errorf("default: want false, got true")
	}
}

func TestChatPlanExecuteEnabled_ExplicitTrue(t *testing.T) {
	for _, raw := range []string{"true", "1", "TRUE"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_plan_execute_enabled": strPtr(raw),
		}}
		if !ChatPlanExecuteEnabled(context.Background(), r) {
			t.Errorf("raw=%q: want true, got false", raw)
		}
	}
}

func TestChatPlanExecuteEnabled_ExplicitFalse(t *testing.T) {
	for _, raw := range []string{"false", "0", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_plan_execute_enabled": strPtr(raw),
		}}
		if ChatPlanExecuteEnabled(context.Background(), r) {
			t.Errorf("raw=%q: want false, got true", raw)
		}
	}
}

func TestChatPlanExecuteEnabled_NilReader(t *testing.T) {
	if ChatPlanExecuteEnabled(context.Background(), nil) {
		t.Errorf("nil reader: want false, got true")
	}
}

func TestChatPlanExecuteMaxSubQueries_DefaultsAndRange(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"3", 3},
		{"1", 1},
		{"5", 5},
		{"0", 3},
		{"6", 3},
		{"abc", 3},
	}
	for _, tc := range cases {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_plan_execute_max_sub_queries": strPtr(tc.raw),
		}}
		if got := ChatPlanExecuteMaxSubQueries(context.Background(), r); got != tc.want {
			t.Errorf("raw=%q: want %d, got %d", tc.raw, tc.want, got)
		}
	}
}

func TestChatPlanExecuteMaxIterations_DefaultsAndRange(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"3", 3},
		{"1", 1},
		{"5", 5},
		{"0", 3},
		{"6", 3},
		{"abc", 3},
	}
	for _, tc := range cases {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_plan_execute_max_iterations": strPtr(tc.raw),
		}}
		if got := ChatPlanExecuteMaxIterations(context.Background(), r); got != tc.want {
			t.Errorf("raw=%q: want %d, got %d", tc.raw, tc.want, got)
		}
	}
}

func TestChatPlanExecuteTokenBudget_DefaultsAndRange(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"8000", 8000},
		{"2000", 2000},
		{"32000", 32000},
		{"1999", 8000},
		{"32001", 8000},
		{"abc", 8000},
	}
	for _, tc := range cases {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_plan_execute_token_budget": strPtr(tc.raw),
		}}
		if got := ChatPlanExecuteTokenBudget(context.Background(), r); got != tc.want {
			t.Errorf("raw=%q: want %d, got %d", tc.raw, tc.want, got)
		}
	}
}

func TestChatPlanExecuteModel_DefaultEmptyInheritsLater(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ChatPlanExecuteModel(context.Background(), r); got != "" {
		t.Errorf("default: want empty (so caller inherits chat_agentic_model), got %q", got)
	}
}

func TestChatPlanExecuteModel_ExplicitOverride(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_plan_execute_model": strPtr("gemma-tiny"),
	}}
	if got := ChatPlanExecuteModel(context.Background(), r); got != "gemma-tiny" {
		t.Errorf("want gemma-tiny, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Answer-time tool calling readers (chat_answer_tools_*)
// ---------------------------------------------------------------------------

func TestChatAnswerToolsEnabled_DefaultOff(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ChatAnswerToolsEnabled(context.Background(), r) {
		t.Error("default must be false")
	}
	if ChatAnswerToolsEnabled(context.Background(), nil) {
		t.Error("nil reader must yield false")
	}
}

func TestChatAnswerToolsEnabled_AcceptsTrueAndOne(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"true", "TRUE", "1", "  true  "} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_answer_tools_enabled": strPtr(v),
		}}
		if !ChatAnswerToolsEnabled(context.Background(), r) {
			t.Errorf("value %q must enable", v)
		}
	}
}

func TestChatAnswerToolsEnabled_AcceptsFalseAndZero(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"false", "FALSE", "0", "  false  "} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_answer_tools_enabled": strPtr(v),
		}}
		if ChatAnswerToolsEnabled(context.Background(), r) {
			t.Errorf("value %q must disable", v)
		}
	}
}

func TestChatAnswerToolsMaxRounds_DefaultAndOutOfRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val  string
		want int
	}{
		{"", 5},  // missing → default
		{"1", 1}, // in range
		{"3", 3},
		{"5", 5},
		{"10", 10},
		{"0", 5},    // out of range → default
		{"-3", 5},   // out of range → default
		{"11", 5},   // out of range → default
		{"99", 5},   // out of range → default
		{"junk", 5}, // unparseable → default
	}
	for _, tc := range cases {
		values := map[string]*string{}
		if tc.val != "" {
			values["chat_answer_tools_max_rounds"] = strPtr(tc.val)
		}
		r := &fakeSiteConfigReader{values: values}
		if got := ChatAnswerToolsMaxRounds(context.Background(), r); got != tc.want {
			t.Errorf("val=%q: got %d, want %d", tc.val, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Retrieval feedback boost config readers (online user feedback loop)
// ---------------------------------------------------------------------------

func TestFeedbackBoostConfig(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_feedback_boost_enabled": strPtr("true"),
		"feedback_boost_weight":       strPtr("0.08"),
	}}
	ctx := context.Background()
	if !ChatFeedbackBoostEnabled(ctx, r) {
		t.Fatal("expected enabled")
	}
	if got := FeedbackBoostWeight(ctx, r); got != 0.08 {
		t.Fatalf("got %v", got)
	}
}

// ---------------------------------------------------------------------------
// HyPE ingest + search accessor tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// describe-image config readers
// ---------------------------------------------------------------------------

func TestDescribeImageConfig(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{
		"describe_image_enabled": strPtr("true"),
		"describe_image_model":   strPtr("qwen2-vl-7b"),
	}}
	ctx := context.Background()
	if !DescribeImageEnabled(ctx, r) {
		t.Fatal("expected enabled")
	}
	if got := DescribeImageModel(ctx, r); got != "qwen2-vl-7b" {
		t.Fatalf("got %q", got)
	}
}

func TestHyPEAccessors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// --- explicit values ---

	t.Run("hype_enabled true", func(t *testing.T) {
		t.Parallel()
		r := &fakeSiteConfigReader{values: map[string]*string{
			"hype_enabled": strPtr("true"),
		}}
		if !HyPEEnabled(ctx, r) {
			t.Error("hype_enabled=true: want true, got false")
		}
	})

	t.Run("hype_search_enabled 1", func(t *testing.T) {
		t.Parallel()
		r := &fakeSiteConfigReader{values: map[string]*string{
			"hype_search_enabled": strPtr("1"),
		}}
		if !HyPESearchEnabled(ctx, r) {
			t.Error("hype_search_enabled=1: want true, got false")
		}
	})

	t.Run("hype_questions_per_chunk 5", func(t *testing.T) {
		t.Parallel()
		r := &fakeSiteConfigReader{values: map[string]*string{
			"hype_questions_per_chunk": strPtr("5"),
		}}
		if got := HyPEQuestionsPerChunk(ctx, r); got != 5 {
			t.Errorf("hype_questions_per_chunk=5: want 5, got %d", got)
		}
	})

	// --- all-empty reader → defaults ---

	t.Run("empty reader defaults", func(t *testing.T) {
		t.Parallel()
		r := &fakeSiteConfigReader{values: map[string]*string{}}
		if HyPEEnabled(ctx, r) {
			t.Error("empty reader: HyPEEnabled want false, got true")
		}
		if HyPESearchEnabled(ctx, r) {
			t.Error("empty reader: HyPESearchEnabled want false, got true")
		}
		if got := HyPEQuestionsPerChunk(ctx, r); got != 3 {
			t.Errorf("empty reader: HyPEQuestionsPerChunk want 3, got %d", got)
		}
	})

	// --- nil reader → defaults ---

	t.Run("nil reader defaults", func(t *testing.T) {
		t.Parallel()
		if HyPEEnabled(ctx, nil) {
			t.Error("nil reader: HyPEEnabled want false, got true")
		}
		if HyPESearchEnabled(ctx, nil) {
			t.Error("nil reader: HyPESearchEnabled want false, got true")
		}
		if got := HyPEQuestionsPerChunk(ctx, nil); got != 3 {
			t.Errorf("nil reader: HyPEQuestionsPerChunk want 3, got %d", got)
		}
	})

	// --- out-of-range clamp → default ---

	t.Run("hype_questions_per_chunk 99 clamps to default", func(t *testing.T) {
		t.Parallel()
		r := &fakeSiteConfigReader{values: map[string]*string{
			"hype_questions_per_chunk": strPtr("99"),
		}}
		if got := HyPEQuestionsPerChunk(ctx, r); got != 3 {
			t.Errorf("hype_questions_per_chunk=99: want default 3, got %d", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Corpus comparison table config readers
// ---------------------------------------------------------------------------

func TestChatCorpusTableAccessors(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_corpus_table_enabled":            strPtr("true"),
		"chat_corpus_table_max_files":          strPtr("12"),
		"chat_corpus_table_concurrency":        strPtr("3"),
		"chat_corpus_table_model":              strPtr("gemma-fast"),
		"chat_corpus_table_router_llm_enabled": strPtr("false"),
	}}
	ctx := context.Background()
	if !ChatCorpusTableEnabled(ctx, r) {
		t.Fatal("expected enabled")
	}
	if got := ChatCorpusTableMaxFiles(ctx, r); got != 12 {
		t.Fatalf("max_files=%d want 12", got)
	}
	if got := ChatCorpusTableConcurrency(ctx, r); got != 3 {
		t.Fatalf("concurrency=%d want 3", got)
	}
	if got := ChatCorpusTableModel(ctx, r); got != "gemma-fast" {
		t.Fatalf("model=%q want gemma-fast", got)
	}
	if ChatCorpusTableRouterLLMEnabled(ctx, r) {
		t.Fatal("expected router LLM disabled")
	}
}

func TestChatCorpusTableDefaults(t *testing.T) {
	t.Parallel()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	ctx := context.Background()
	if ChatCorpusTableEnabled(ctx, r) {
		t.Fatal("default should be disabled")
	}
	if got := ChatCorpusTableMaxFiles(ctx, r); got != 50 {
		t.Fatalf("default max_files=%d want 50", got)
	}
	if got := ChatCorpusTableConcurrency(ctx, r); got != 6 {
		t.Fatalf("default concurrency=%d want 6", got)
	}
	if !ChatCorpusTableRouterLLMEnabled(ctx, r) {
		t.Fatal("router LLM should default on")
	}
}

func TestChatCorpusTableClamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// chat_corpus_table_max_files = "0" is below the valid range [1, 500];
	// readInt falls back to the default (50) for out-of-range values rather
	// than clamping, so the accessor must return 50, not 0 and not 1.
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_corpus_table_max_files": strPtr("0"),
	}}
	if got := ChatCorpusTableMaxFiles(ctx, r); got != 50 {
		t.Fatalf("max_files with value 0 (below min): got %d, want 50 (default)", got)
	}
}

// ---------------------------------------------------------------------------
// Date-aware chat readers tests
// ---------------------------------------------------------------------------

func TestChatDateReaders(t *testing.T) {
	ctx := context.Background()

	// Defaults (nil reader → all defaults).
	if got := ChatDateAwarenessEnabled(ctx, nil); got != true {
		t.Errorf("ChatDateAwarenessEnabled default = %v, want true", got)
	}
	if got := ChatDateTimezone(ctx, nil); got != "Europe/Berlin" {
		t.Errorf("ChatDateTimezone default = %q, want Europe/Berlin", got)
	}
	if got := ChatDateToolsEnabled(ctx, nil); got != false {
		t.Errorf("ChatDateToolsEnabled default = %v, want false", got)
	}
	if got := ChatDateToolsMaxResults(ctx, nil); got != 50 {
		t.Errorf("ChatDateToolsMaxResults default = %d, want 50", got)
	}

	// Overrides via a struct-backed fake reader.
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_date_awareness_enabled": strPtr("false"),
		"chat_date_timezone":          strPtr("UTC"),
		"chat_date_tools_enabled":     strPtr("true"),
		"chat_date_tools_max_results": strPtr("10"),
	}}
	if ChatDateAwarenessEnabled(ctx, r) != false {
		t.Error("awareness override not applied")
	}
	if ChatDateTimezone(ctx, r) != "UTC" {
		t.Error("timezone override not applied")
	}
	if ChatDateToolsEnabled(ctx, r) != true {
		t.Error("tools-enabled override not applied")
	}
	if ChatDateToolsMaxResults(ctx, r) != 10 {
		t.Error("max-results override not applied")
	}

	// Blank timezone falls back to default.
	if got := ChatDateTimezone(ctx, &fakeSiteConfigReader{values: map[string]*string{"chat_date_timezone": strPtr("  ")}}); got != "Europe/Berlin" {
		t.Errorf("blank timezone = %q, want Europe/Berlin", got)
	}
}

// ---------------------------------------------------------------------------
// Recency-listing readers tests
// ---------------------------------------------------------------------------

func TestChatRecencyListingReaders(t *testing.T) {
	ctx := context.Background()

	// Defaults (nil reader): correctness fix → on by default, 7-day
	// window, 50-entry listing cap.
	if got := ChatRecencyListingEnabled(ctx, nil); got != true {
		t.Errorf("ChatRecencyListingEnabled default = %v, want true", got)
	}
	if got := ChatRecencyListingWindowDays(ctx, nil); got != 7 {
		t.Errorf("ChatRecencyListingWindowDays default = %d, want 7", got)
	}
	if got := ChatRecencyListingMaxResults(ctx, nil); got != 50 {
		t.Errorf("ChatRecencyListingMaxResults default = %d, want 50", got)
	}

	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_recency_listing_enabled":     strPtr("false"),
		"chat_recency_listing_window_days": strPtr("3"),
		"chat_recency_listing_max_results": strPtr("100"),
	}}
	if ChatRecencyListingEnabled(ctx, r) != false {
		t.Error("enabled override not applied")
	}
	if ChatRecencyListingWindowDays(ctx, r) != 3 {
		t.Error("window-days override not applied")
	}
	if ChatRecencyListingMaxResults(ctx, r) != 100 {
		t.Error("max-results override not applied")
	}

	// Out-of-range values fall back to the default (readInt semantics).
	if got := ChatRecencyListingWindowDays(ctx, &fakeSiteConfigReader{values: map[string]*string{"chat_recency_listing_window_days": strPtr("9999")}}); got != 7 {
		t.Errorf("window-days out-of-range = %d, want default 7", got)
	}

	// Name-marker lookup: default ON, kill switch.
	if got := ChatRecencyListingNameMatchEnabled(ctx, nil); got != true {
		t.Errorf("ChatRecencyListingNameMatchEnabled default = %v, want true", got)
	}
	offNM := &fakeSiteConfigReader{values: map[string]*string{"chat_recency_listing_name_match_enabled": strPtr("false")}}
	if ChatRecencyListingNameMatchEnabled(ctx, offNM) != false {
		t.Error("name-match override not applied")
	}
}
