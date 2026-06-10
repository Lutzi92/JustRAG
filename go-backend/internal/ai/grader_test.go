package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestParseGraderResponse_StrictJSON exercises the happy path.
func TestParseGraderResponse_StrictJSON(t *testing.T) {
	t.Parallel()
	got := parseGraderResponse(`{"grades": ["relevant", "ambiguous", "irrelevant"]}`, 3)
	want := []Grade{GradeRelevant, GradeAmbiguous, GradeIrrelevant}
	for i, g := range want {
		if got[i] != g {
			t.Errorf("idx %d: got %q, want %q", i, got[i], g)
		}
	}
}

// TestParseGraderResponse_MarkdownFenced verifies tolerance of ```json fences,
// which DeepSeek and several other OpenAI-compat providers add despite explicit
// "no markdown" instructions.
func TestParseGraderResponse_MarkdownFenced(t *testing.T) {
	t.Parallel()
	body := "```json\n" + `{"grades": ["relevant", "irrelevant"]}` + "\n```"
	got := parseGraderResponse(body, 2)
	if got[0] != GradeRelevant || got[1] != GradeIrrelevant {
		t.Errorf("got %v", got)
	}
}

// TestParseGraderResponse_LengthMismatch verifies tail-padding with Ambiguous
// when the model returns fewer entries than requested. CRAG must never lose a
// chunk to a short grader response.
func TestParseGraderResponse_LengthMismatch(t *testing.T) {
	t.Parallel()
	got := parseGraderResponse(`{"grades": ["relevant"]}`, 3)
	if len(got) != 3 {
		t.Fatalf("expected length 3, got %d", len(got))
	}
	if got[0] != GradeRelevant {
		t.Errorf("idx 0: got %q, want %q", got[0], GradeRelevant)
	}
	if got[1] != GradeAmbiguous || got[2] != GradeAmbiguous {
		t.Errorf("expected tail Ambiguous, got %v", got)
	}
}

// TestParseGraderResponse_BareArray verifies acceptance of a top-level array
// when the model omits the wrapping object.
func TestParseGraderResponse_BareArray(t *testing.T) {
	t.Parallel()
	got := parseGraderResponse(`["relevant", "relevant"]`, 2)
	if got[0] != GradeRelevant || got[1] != GradeRelevant {
		t.Errorf("got %v", got)
	}
}

// TestParseGraderResponse_UnknownLabel coerces unknown values to Ambiguous.
// Grader hallucinations like "highly_relevant" must not blank a chunk out.
func TestParseGraderResponse_UnknownLabel(t *testing.T) {
	t.Parallel()
	got := parseGraderResponse(`{"grades": ["highly_relevant", "RELEVANT", "trash"]}`, 3)
	want := []Grade{GradeAmbiguous, GradeRelevant, GradeAmbiguous}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseGraderResponse_HardFailure fail-opens to all Ambiguous so the CRAG
// pipeline never blocks on a broken grader.
func TestParseGraderResponse_HardFailure(t *testing.T) {
	t.Parallel()
	got := parseGraderResponse(`completely garbled output, no json anywhere`, 4)
	if len(got) != 4 {
		t.Fatalf("expected length 4, got %d", len(got))
	}
	for i, g := range got {
		if g != GradeAmbiguous {
			t.Errorf("idx %d: got %q, want %q (fail-open)", i, g, GradeAmbiguous)
		}
	}
}

// TestParseGraderResponse_EmbeddedNoise extracts the grades array even when the
// model wraps the JSON in commentary.
func TestParseGraderResponse_EmbeddedNoise(t *testing.T) {
	t.Parallel()
	body := `Here are the grades: {"grades": ["relevant", "irrelevant"]}. Hope that helps!`
	got := parseGraderResponse(body, 2)
	if got[0] != GradeRelevant || got[1] != GradeIrrelevant {
		t.Errorf("got %v", got)
	}
}

// TestCountByGrade verifies the bucket tally used by metrics + decision logic.
func TestCountByGrade(t *testing.T) {
	t.Parallel()
	rel, amb, irr := CountByGrade([]Grade{
		GradeRelevant, GradeRelevant, GradeAmbiguous, GradeIrrelevant, GradeIrrelevant, GradeIrrelevant,
	})
	if rel != 2 || amb != 1 || irr != 3 {
		t.Errorf("got rel=%d amb=%d irr=%d, want 2/1/3", rel, amb, irr)
	}
}

// TestStripJSONFences covers the variants we've actually seen from providers.
func TestStripJSONFences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"json-fence", "```json\n{\"x\":1}\n```", `{"x":1}`},
		{"plain-fence", "```\n{\"x\":1}\n```", `{"x":1}`},
		{"no-fence", `{"x":1}`, `{"x":1}`},
		{"json-fence-with-padding", "   ```json\n{\"x\":1}\n```\n  ", `{"x":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripJSONFences(tc.in); got != tc.want {
				t.Errorf("stripJSONFences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestGradeRelevance_SendsStructuredOutputContract(t *testing.T) {
	var captured map[string]any
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatJSON(w, chatResponse(`{"grades":["relevant","irrelevant"]}`, ""))
	})
	resolver := resolverForServer(srv, "fast-model")

	grades, err := GradeRelevance(context.Background(), resolver, "q", []string{"a", "b"}, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(grades) != 2 || grades[0] != GradeRelevant || grades[1] != GradeIrrelevant {
		t.Errorf("grades = %v", grades)
	}

	rf, ok := captured["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("grader request carried no response_format: %v", captured)
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
}
