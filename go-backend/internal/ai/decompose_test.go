package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestGenerateSubQueries_HappyPath verifies the parsing + cap pipeline
// on a well-formed JSON array response. Three sub-questions go in,
// three come out.
func TestGenerateSubQueries_HappyPath(t *testing.T) {
	jsonResp, _ := json.Marshal([]string{
		"What projects is Alice on?",
		"What is the budget of each project?",
		"Who leads each project?",
	})

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(string(jsonResp), ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	subQ, err := GenerateSubQueries(context.Background(), resolver, "Which of Alice's projects has the highest budget and who leads it?", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subQ) != 3 {
		t.Fatalf("expected 3 sub-questions, got %d: %v", len(subQ), subQ)
	}
}

// TestGenerateSubQueries_CapsAtFour confirms the maxDecomposedSubQueries
// post-trim cap. The prompt asks for 2-4 sub-questions but a misbehaving
// model could emit more; the cap prevents runaway fan-out.
func TestGenerateSubQueries_CapsAtFour(t *testing.T) {
	jsonResp, _ := json.Marshal([]string{"q1", "q2", "q3", "q4", "q5", "q6"})

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(string(jsonResp), ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	subQ, err := GenerateSubQueries(context.Background(), resolver, "topic", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subQ) != maxDecomposedSubQueries {
		t.Errorf("expected max %d sub-questions, got %d", maxDecomposedSubQueries, len(subQ))
	}
}

// TestGenerateSubQueries_EmptyArrayMeansSingleAspect pins the prompt
// contract: an empty array tells the caller the query has only one
// aspect and decomposition would be harmful. The function must
// surface that as len(out)==0, not as an error.
func TestGenerateSubQueries_EmptyArrayMeansSingleAspect(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("[]", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	subQ, err := GenerateSubQueries(context.Background(), resolver, "What is the project ID of the Stud.IP update?", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subQ) != 0 {
		t.Errorf("expected empty result for single-aspect query, got %v", subQ)
	}
}

// TestGenerateSubQueries_DropsEmptyEntries verifies whitespace-only /
// empty sub-questions are filtered. Mid-tier models occasionally emit
// a "" entry inside an otherwise valid array; those should not show
// up in opts.SubQueries as empty retrieval queries (which would fan
// out as zero-result vector searches).
func TestGenerateSubQueries_DropsEmptyEntries(t *testing.T) {
	jsonResp, _ := json.Marshal([]string{"q1", "", "  ", "q2"})

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(string(jsonResp), ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	subQ, err := GenerateSubQueries(context.Background(), resolver, "topic", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subQ) != 2 {
		t.Errorf("expected 2 sub-questions after dropping empties, got %d: %v", len(subQ), subQ)
	}
	for _, q := range subQ {
		if q == "" {
			t.Errorf("empty sub-question survived filter: %v", subQ)
		}
	}
}

// TestGenerateSubQueries_ParseFailureIsNotError mirrors the multi-query
// contract: malformed LLM output yields an empty slice + nil error,
// so the caller can treat decomposition as best-effort and proceed
// to retrieval without sub-questions.
func TestGenerateSubQueries_ParseFailureIsNotError(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("not a json array at all", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	subQ, err := GenerateSubQueries(context.Background(), resolver, "topic", "", "en")
	if err != nil {
		t.Fatalf("parse failure should not propagate as error: %v", err)
	}
	if len(subQ) != 0 {
		t.Errorf("expected empty slice on parse failure, got %v", subQ)
	}
}
