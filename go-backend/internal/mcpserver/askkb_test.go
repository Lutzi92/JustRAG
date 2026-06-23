package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeAnswerer struct {
	gotKB, gotQuestion, gotLang string
	result                      AnswerResult
	err                         error
}

func (f *fakeAnswerer) Answer(_ context.Context, kbID, question, language string) (AnswerResult, error) {
	f.gotKB, f.gotQuestion, f.gotLang = kbID, question, language
	return f.result, f.err
}

func TestRunAskKB_Success(t *testing.T) {
	fa := &fakeAnswerer{result: AnswerResult{
		Answer:  "the answer",
		Sources: []Source{{Index: 1, FileID: "f1", FileName: "doc.pdf", Score: 0.9}},
	}}
	res, err := runAskKB(context.Background(), fa, "kb-123", json.RawMessage(`{"question":"q?","language":"en"}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fa.gotKB != "kb-123" {
		t.Errorf("kbID = %q, want kb-123 (must come from path, not params)", fa.gotKB)
	}
	if fa.gotQuestion != "q?" || fa.gotLang != "en" {
		t.Errorf("question/lang = %q/%q", fa.gotQuestion, fa.gotLang)
	}
	if res.IsError {
		t.Errorf("IsError = true, want false")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "the answer" {
		t.Errorf("content = %+v", res.Content)
	}
	var sc struct {
		Sources []Source `json:"sources"`
	}
	if err := json.Unmarshal(res.StructuredContent, &sc); err != nil {
		t.Fatalf("structuredContent unmarshal: %v", err)
	}
	if len(sc.Sources) != 1 || sc.Sources[0].FileName != "doc.pdf" {
		t.Errorf("sources = %+v", sc.Sources)
	}
}

func TestRunAskKB_MissingQuestion(t *testing.T) {
	_, err := runAskKB(context.Background(), &fakeAnswerer{}, "kb-1", json.RawMessage(`{"language":"de"}`))
	if err == nil {
		t.Fatal("err = nil, want invalid-params error")
	}
}

func TestRunAskKB_PipelineError(t *testing.T) {
	fa := &fakeAnswerer{err: errors.New("boom")}
	res, err := runAskKB(context.Background(), fa, "kb-1", json.RawMessage(`{"question":"q?"}`))
	if err != nil {
		t.Fatalf("err should be nil (pipeline failure is reported via IsError), got %v", err)
	}
	if !res.IsError {
		t.Error("IsError = false, want true")
	}
	if len(res.Content) == 0 {
		t.Error("expected an error text block")
	}
}

func TestRunAskKB_DefaultLanguage(t *testing.T) {
	fa := &fakeAnswerer{}
	if _, err := runAskKB(context.Background(), fa, "kb-1", json.RawMessage(`{"question":"q?"}`)); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fa.gotLang != "de" {
		t.Errorf("default lang = %q, want de", fa.gotLang)
	}
}
