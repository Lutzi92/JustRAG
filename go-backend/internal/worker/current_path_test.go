package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
)

type pathLinks struct {
	fakeLinks
	path string
	perr error
}

func (p *pathLinks) CurrentStoragePath(context.Context, string) (string, error) {
	return p.path, p.perr
}

type existsStorage struct {
	fakeRSSStorage
	have map[string]bool
	asks []string
}

func (s *existsStorage) FileExists(_ context.Context, p string) (bool, error) {
	s.asks = append(s.asks, p)
	return s.have[p], nil
}

func TestHandlerUsesCurrentStoragePath(t *testing.T) {
	for _, typ := range []string{jobs.TypeFileProcessing, jobs.TypeReEmbedding} {
		fp := &fakeFileProcessor{}
		h := NewFileProcessingHandlerWithDeps(FileProcessingDeps{
			Proc: fp, Links: &pathLinks{path: "users/u/new"},
		})
		raw, _ := json.Marshal(jobs.FileProcessingPayload{FileID: "f", KbID: "k", FilePath: "old/path"})
		if err := h(context.Background(), asynq.NewTask(typ, raw)); err != nil {
			t.Fatal(err)
		}
		if fp.got.FilePath != "users/u/new" {
			t.Errorf("%s: processor path = %q, want the current path", typ, fp.got.FilePath)
		}
	}
}

func TestHandlerKeepsPayloadPathOnLookupError(t *testing.T) {
	fp := &fakeFileProcessor{}
	h := NewFileProcessingHandlerWithDeps(FileProcessingDeps{
		Proc: fp, Links: &pathLinks{perr: errors.New("db down")},
	})
	raw, _ := json.Marshal(jobs.FileProcessingPayload{FileID: "f", KbID: "k", FilePath: "old/path"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeReEmbedding, raw)); err != nil {
		t.Fatal(err)
	}
	if fp.got.FilePath != "old/path" {
		t.Errorf("path = %q, want payload path", fp.got.FilePath)
	}
}

func TestPreflightReembed(t *testing.T) {
	pl := jobs.FileProcessingPayload{FileID: "f", FilePath: "old/path"}
	// Stale payload path, blob only at the current path: ok, current path asked.
	st := &existsStorage{have: map[string]bool{"users/u/new": true}}
	if err := PreflightReembed(context.Background(), &pathLinks{path: "users/u/new"}, st, pl); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(st.asks) != 1 || st.asks[0] != "users/u/new" {
		t.Errorf("asked %v", st.asks)
	}
	// Missing blob: error (caller must not touch the index).
	st = &existsStorage{have: map[string]bool{}}
	if err := PreflightReembed(context.Background(), &pathLinks{path: "users/u/new"}, st, pl); err == nil {
		t.Fatal("missing blob must fail the preflight")
	}
	// No path at all: error.
	if err := PreflightReembed(context.Background(), &pathLinks{}, st, jobs.FileProcessingPayload{FileID: "f"}); err == nil {
		t.Fatal("empty path must fail")
	}
}
