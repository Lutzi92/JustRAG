package ai

// Structured-Outputs contract tests for the fast-tier JSON call sites,
// modeled on TestGradeRelevance_SendsStructuredOutputContract: an
// httptest server captures the wire request, returns a canned valid
// JSON response, and each test asserts (a) the request carried a
// strict response_format of type json_schema and (b) the function's
// tolerant parsing still produces the right result.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

// captureStructuredRequest wires a chat server that records the raw
// request body and answers with the canned content string.
func captureStructuredRequest(t *testing.T, content string) (*ConfigResolver, *map[string]any) {
	t.Helper()
	captured := &map[string]any{}
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeChatJSON(w, chatResponse(content, ""))
	})
	return resolverForServer(srv, "fast-model"), captured
}

// assertJSONSchemaFormat fails the test unless the captured request
// carried response_format.type == "json_schema".
func assertJSONSchemaFormat(t *testing.T, captured map[string]any) {
	t.Helper()
	rf, ok := captured["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no response_format: %v", captured)
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
}

func TestRouteKB_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"matches":[{"kb_id":"kb-1","confidence":0.9,"reason":"fits"}]}`)

	matches, err := RouteKB(context.Background(), resolver, "q",
		[]prompts.KBRouterCandidate{{ID: "kb-1", Name: "A"}}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 || matches[0].KBID != "kb-1" || matches[0].Confidence != 0.9 {
		t.Errorf("matches = %+v", matches)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestClassifyLongmemConflict_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"action":"supersede","supersede_ids":[42]}`)

	v, err := ClassifyLongmemConflict(context.Background(), resolver,
		LongmemCandidate{ID: 1, Kind: "semantic", Content: "user works on B"},
		[]LongmemCandidate{{ID: 42, Kind: "semantic", Content: "user works on A"}},
		"", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != LongmemConflictSupersede || len(v.SupersedeIDs) != 1 || v.SupersedeIDs[0] != 42 {
		t.Errorf("verdict = %+v", v)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestGenerateSubQueries_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t, `["who leads X?","what budget?"]`)

	queries, err := GenerateSubQueries(context.Background(), resolver, "complex q", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 2 || queries[0] != "who leads X?" {
		t.Errorf("queries = %v", queries)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestPlanQueries_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"queries":["q1","q2"],"rationale":"split"}`)

	queries, rationale, err := PlanQueries(context.Background(), resolver, "", "question", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 2 || rationale != "split" {
		t.Errorf("queries = %v, rationale = %q", queries, rationale)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestPlanQueriesDAG_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"nodes":[{"id":"n1","query":"q1","depends_on":[],"reason":"r"}],"rationale":"x"}`)

	plan, err := PlanQueriesDAG(context.Background(), resolver, "", "question", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Nodes) != 1 || plan.Nodes[0].ID != "n1" || plan.Rationale != "x" {
		t.Errorf("plan = %+v", plan)
	}
	assertJSONSchemaFormat(t, *captured)
}

// TestPlanQueriesDAGToolAware_StaysUnstructured pins the deliberate
// exception: the tool-aware planner's per-node `args` is a free-form
// per-tool JSON object (map[string]any), which strict-mode json_schema
// cannot express — `additionalProperties: false` would force args to
// be empty and break calculator/sql_query nodes. The call stays on the
// plain completion path.
func TestPlanQueriesDAGToolAware_StaysUnstructured(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"nodes":[{"id":"n1","query":"","tool":"calculator","args":{"expression":"1+1"},"depends_on":[],"reason":"r"}],"rationale":"x"}`)

	plan, err := PlanQueriesDAGToolAware(context.Background(), resolver, "", "question", "en", "",
		[]prompts.PlanToolEntry{{Name: "calculator", Description: "math"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Nodes) != 1 || plan.Nodes[0].Tool != "calculator" || plan.Nodes[0].Args["expression"] != "1+1" {
		t.Errorf("plan = %+v", plan)
	}
	if _, has := (*captured)["response_format"]; has {
		t.Errorf("tool-aware planner must not send response_format (free-form args), got %v", (*captured)["response_format"])
	}
}

func TestCritiqueDAGProgress_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"action":"answer_now","new_nodes":[],"prune_node_ids":[],"reason":"enough"}`)

	res, err := CritiqueDAGProgress(context.Background(), resolver, "q",
		nil, nil, map[string]bool{}, map[string]bool{}, "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != CritiqueActionAnswerNow {
		t.Errorf("action = %q, want answer_now", res.Action)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestVerifyFactuality_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"flagged_claims":[{"claim_text":"the sky is green","reason":"contradicted","cited_n":2}]}`)

	claims, err := VerifyFactuality(context.Background(), resolver, "q", "a", "sources", "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(claims) != 1 || claims[0].Reason != "contradicted" || claims[0].CitedN != 2 {
		t.Errorf("claims = %+v", claims)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestVerifySelfRAG_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"isrel":[{"n":1,"verdict":"relevant"}],"issup":[],"isuse":{"verdict":"yes","reason":"ok"}}`)

	res, err := VerifySelfRAG(context.Background(), resolver, "q", "a", "sources", "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Relevance) != 1 || res.Relevance[0].Verdict != "relevant" || res.Usefulness.Verdict != "yes" {
		t.Errorf("result = %+v", res)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestExtractSalientFacts_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"facts":[{"kind":"semantic","content":"user prefers German answers","salience":0.8}]}`)

	facts, err := ExtractSalientFacts(context.Background(), resolver, "user msg", "assistant answer", "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 || facts[0].Kind != "semantic" || facts[0].Salience != 0.8 {
		t.Errorf("facts = %+v", facts)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestExtractKG_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"entities":[{"name":"Alice","type":"person","aliases":["A."]}],"relations":[{"src":"Alice","rel":"leads","dst":"Alice","evidence":"Alice leads"}]}`)

	out, err := ExtractKG(context.Background(), resolver, "doc.pdf", "body", "chunk", "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Entities) != 1 || out.Entities[0].Name != "Alice" || out.Entities[0].Type != "person" {
		t.Errorf("extraction = %+v", out)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestDecideNextAction_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"action":"search","queries":["follow-up"],"reason":"gap"}`)

	action, queries, reason, err := DecideNextAction(context.Background(), resolver, "", "q", "chunks", 1, "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "search" || len(queries) != 1 || reason != "gap" {
		t.Errorf("action=%q queries=%v reason=%q", action, queries, reason)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestNeedsMoreInfo_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t,
		`{"sufficient":false,"follow_up_query":"missing detail"}`)

	sufficient, followUp, err := NeedsMoreInfo(context.Background(), resolver, "", "q", "chunks", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sufficient || followUp != "missing detail" {
		t.Errorf("sufficient=%v followUp=%q", sufficient, followUp)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestScoreEvidentiality_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t, `{"scores":[0.9,0.1]}`)

	scores, err := ScoreEvidentiality(context.Background(), resolver, "q", []string{"a", "b"}, "", "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.1 {
		t.Errorf("scores = %v", scores)
	}
	assertJSONSchemaFormat(t, *captured)
}

func TestGenerateHypotheticalQuestions_SendsStructuredOutputContract(t *testing.T) {
	resolver, captured := captureStructuredRequest(t, `["What is X?","Who owns Y?"]`)

	questions, err := GenerateHypotheticalQuestions(context.Background(), resolver,
		"doc.pdf", "document body", "chunk", "", 3, "en", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(questions) != 2 || questions[0] != "What is X?" {
		t.Errorf("questions = %v", questions)
	}
	assertJSONSchemaFormat(t, *captured)
}
