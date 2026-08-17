package openaicompat

import (
	"encoding/json"
	"testing"
)

const (
	testCompletionID = "chatcmpl-test"
	testModel        = "kb-abc"
	testCreated      = int64(1700000000)
	testAnswer       = "Der Wert ist 42 [1]."
)

// decode marshals v and re-parses it as a generic map, so assertions run
// against the bytes a client actually receives rather than the Go struct.
func decode(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func firstChoice(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("no choices in payload: %v", payload)
	}
	c, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("choice is not an object: %v", choices[0])
	}
	return c
}

// ---------------------------------------------------------------------------
// Non-streaming
// ---------------------------------------------------------------------------

func TestBuildCompletionResponseCarriesAnnotationsAndCitations(t *testing.T) {
	resp := buildCompletionResponse(testCompletionID, testModel, testCreated, testAnswer, testSources())

	msg := firstChoice(t, decode(t, resp))["message"].(map[string]any)

	if msg["content"] != testAnswer {
		t.Errorf("content = %v, want %q", msg["content"], testAnswer)
	}

	anns, ok := msg["annotations"].([]any)
	if !ok || len(anns) != 1 {
		t.Fatalf("annotations = %v, want 1 entry", msg["annotations"])
	}
	a := anns[0].(map[string]any)
	if a["type"] != "file_citation" {
		t.Errorf("annotation type = %v, want file_citation", a["type"])
	}
	fc := a["file_citation"].(map[string]any)
	if fc["file_id"] != "f1" || fc["filename"] != "handbuch.pdf" {
		t.Errorf("file_citation = %v, want f1/handbuch.pdf", fc)
	}

	cits := msg["context"].(map[string]any)["citations"].([]any)
	if len(cits) != 2 {
		t.Fatalf("citations = %d, want 2", len(cits))
	}
	if got := cits[0].(map[string]any)["content"]; got != "Erster Chunk." {
		t.Errorf("first citation content = %v, want %q", got, "Erster Chunk.")
	}
}

// A KB that retrieved nothing must not grow empty `annotations` / `context`
// keys — clients branch on their presence.
func TestBuildCompletionResponseOmitsCitationFieldsWithoutSources(t *testing.T) {
	resp := buildCompletionResponse(testCompletionID, testModel, testCreated, "Keine Treffer.", nil)

	msg := firstChoice(t, decode(t, resp))["message"].(map[string]any)
	if _, present := msg["annotations"]; present {
		t.Errorf("annotations key present with no sources: %v", msg)
	}
	if _, present := msg["context"]; present {
		t.Errorf("context key present with no sources: %v", msg)
	}
}

func TestBuildCompletionResponseKeepsOpenAIEnvelope(t *testing.T) {
	payload := decode(t, buildCompletionResponse(testCompletionID, testModel, testCreated, testAnswer, testSources()))

	if payload["object"] != "chat.completion" {
		t.Errorf("object = %v, want chat.completion", payload["object"])
	}
	if payload["id"] != testCompletionID {
		t.Errorf("id = %v, want %q", payload["id"], testCompletionID)
	}
	if payload["model"] != testModel {
		t.Errorf("model = %v, want %q", payload["model"], testModel)
	}
	choice := firstChoice(t, payload)
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
	}
	if choice["message"].(map[string]any)["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", choice["message"])
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// Sources are known before the first token, so the opening chunk carries them
// and a client can render source cards while the answer is still streaming.
func TestInitialChunkCarriesCitations(t *testing.T) {
	chunk := initialChunk(testCompletionID, testModel, testCreated, testSources())

	payload := decode(t, chunk)
	if payload["object"] != "chat.completion.chunk" {
		t.Errorf("object = %v, want chat.completion.chunk", payload["object"])
	}
	delta := firstChoice(t, payload)["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", delta["role"])
	}
	cits := delta["context"].(map[string]any)["citations"].([]any)
	if len(cits) != 2 {
		t.Errorf("citations = %d, want 2", len(cits))
	}
	if _, present := delta["annotations"]; present {
		t.Errorf("annotations must not appear in the opening chunk: %v", delta)
	}
}

func TestInitialChunkOmitsContextWithoutSources(t *testing.T) {
	delta := firstChoice(t, decode(t, initialChunk(testCompletionID, testModel, testCreated, nil)))["delta"].(map[string]any)
	if _, present := delta["context"]; present {
		t.Errorf("context key present with no sources: %v", delta)
	}
	if delta["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", delta["role"])
	}
}

// Offsets only exist once the whole answer is assembled, so annotations ride
// the final chunk alongside finish_reason.
func TestFinalChunkCarriesAnnotations(t *testing.T) {
	chunk := finalChunk(testCompletionID, testModel, testCreated, testAnswer, testSources())

	choice := firstChoice(t, decode(t, chunk))
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
	}
	anns := choice["delta"].(map[string]any)["annotations"].([]any)
	if len(anns) != 1 {
		t.Fatalf("annotations = %v, want 1 entry", anns)
	}
	fc := anns[0].(map[string]any)["file_citation"].(map[string]any)
	if fc["index"] != float64(16) {
		t.Errorf("index = %v, want 16", fc["index"])
	}
}

func TestFinalChunkOmitsAnnotationsWhenAnswerCitesNothing(t *testing.T) {
	chunk := finalChunk(testCompletionID, testModel, testCreated, "Dazu liegen keine Informationen vor.", testSources())

	choice := firstChoice(t, decode(t, chunk))
	if _, present := choice["delta"].(map[string]any)["annotations"]; present {
		t.Errorf("annotations key present for an uncited answer: %v", choice)
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
	}
}

// Guards the plumbing the whole feature hangs on: the sources computed by the
// RAG pipeline must reach the wire. Passing nil here is the regression that
// leaves clients with no citations at all.
func TestBuildersPropagateSourcesEndToEnd(t *testing.T) {
	sources := testSources()

	nonStream := firstChoice(t, decode(t, buildCompletionResponse(testCompletionID, testModel, testCreated, testAnswer, sources)))
	nonStreamCits := nonStream["message"].(map[string]any)["context"].(map[string]any)["citations"].([]any)

	streamCits := firstChoice(t, decode(t, initialChunk(testCompletionID, testModel, testCreated, sources)))["delta"].(map[string]any)["context"].(map[string]any)["citations"].([]any)

	if len(nonStreamCits) != len(streamCits) {
		t.Errorf("streaming and non-streaming disagree: %d vs %d citations", len(streamCits), len(nonStreamCits))
	}
}
