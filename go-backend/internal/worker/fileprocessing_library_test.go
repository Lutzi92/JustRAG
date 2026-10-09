package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/processor"
)

type fakeFileProcessor struct {
	got     processor.ProcessFileInput
	outcome processor.ProcessOutcome
	err     error
}

func (f *fakeFileProcessor) ProcessFileWithResult(_ context.Context, in processor.ProcessFileInput) (processor.ProcessOutcome, error) {
	f.got = in
	return f.outcome, f.err
}

type fakeOwners struct {
	calls int
	owner string
	err   error
}

func (f *fakeOwners) UserFileOwner(context.Context, string) (string, error) {
	f.calls++
	return f.owner, f.err
}

func runHandler(t *testing.T, fp *fakeFileProcessor, ow *fakeOwners, pl jobs.FileProcessingPayload) error {
	t.Helper()
	raw, _ := json.Marshal(pl)
	h := NewFileProcessingHandlerWithOwners(fp, nil, nil, ow)
	return h(context.Background(), asynq.NewTask(jobs.TypeFileProcessing, raw))
}

func addCount(mode string) float64 {
	return testutil.ToFloat64(observability.UserFileAddTotalForTest().WithLabelValues(mode))
}

func TestFileHandler_LibraryPayloadRecordsModeAndPassesOwner(t *testing.T) {
	for _, tc := range []struct {
		hit  bool
		mode string
	}{{true, "ingest_cached_parse"}, {false, "ingest"}} {
		fp := &fakeFileProcessor{outcome: processor.ProcessOutcome{ParseCacheHit: tc.hit}}
		ow := &fakeOwners{owner: "owner-9"}
		before := addCount(tc.mode)
		if err := runHandler(t, fp, ow, jobs.FileProcessingPayload{FileID: "f", KbID: "k", UserFileID: "uf-1"}); err != nil {
			t.Fatal(err)
		}
		if fp.got.UserFileID != "uf-1" || fp.got.OwnerUserID != "owner-9" {
			t.Errorf("input = %+v", fp.got)
		}
		if d := addCount(tc.mode) - before; d != 1 {
			t.Errorf("%s delta = %v, want 1", tc.mode, d)
		}
	}
}

func TestFileHandler_OwnerLookupFailureProceedsWithoutOwner(t *testing.T) {
	fp := &fakeFileProcessor{}
	ow := &fakeOwners{err: errors.New("db down")}
	if err := runHandler(t, fp, ow, jobs.FileProcessingPayload{FileID: "f", UserFileID: "uf-1"}); err != nil {
		t.Fatal(err)
	}
	if fp.got.OwnerUserID != "" {
		t.Errorf("owner = %q, want empty", fp.got.OwnerUserID)
	}
}

func TestFileHandler_NonLibraryPayloadNoLookupNoMetric(t *testing.T) {
	fp := &fakeFileProcessor{outcome: processor.ProcessOutcome{ParseCacheHit: true}}
	ow := &fakeOwners{owner: "x"}
	b1, b2 := addCount("ingest"), addCount("ingest_cached_parse")
	if err := runHandler(t, fp, ow, jobs.FileProcessingPayload{FileID: "f", KbID: "k"}); err != nil {
		t.Fatal(err)
	}
	if ow.calls != 0 {
		t.Error("owner lookup must not be called")
	}
	if addCount("ingest") != b1 || addCount("ingest_cached_parse") != b2 {
		t.Error("no metric expected")
	}
}

type fakeLinks struct {
	calls     int
	uf, owner string
	err       error
}

func (f *fakeLinks) LibraryLink(context.Context, string) (string, string, error) {
	f.calls++
	return f.uf, f.owner, f.err
}

// runLinked runs the handler with a files-row link lookup and copy mode wired.
func runLinked(t *testing.T, taskType string, links *fakeLinks, pl jobs.FileProcessingPayload) (*copyRig, error) {
	t.Helper()
	r := newCopyRig()
	raw, _ := json.Marshal(pl)
	ing := &recordingProcessor{inner: r.ing, called: &r.ingested}
	ow := &fakeOwners{owner: "payload-owner"}
	h := NewFileProcessingHandlerWithDeps(FileProcessingDeps{
		Proc:   ing,
		Owners: ow,
		Links:  links,
		Copy:   &CopyDeps{Proc: r.proc, Store: r.store, Index: r.idx, Reader: nilReader{}},
	})
	err := h(context.Background(), asynq.NewTask(taskType, raw))
	if ow.calls != 0 {
		t.Errorf("Owners must not be consulted when Links is set (calls=%d)", ow.calls)
	}
	return r, err
}

// A re-embed / retry payload carries no UserFileID; the link is read from the
// files row so the ingest re-stamps the fingerprint and uses the caches — but
// a re-embed never copies (P2-R6) and is not an add.
func TestFileHandler_ReEmbedResolvesLibraryLinkNoCopy(t *testing.T) {
	links := &fakeLinks{uf: "uf-1", owner: "owner-9"}
	pl := libPayload()
	pl.UserFileID = ""
	before := addCount("ingest")
	r, err := runLinked(t, jobs.TypeReEmbedding, links, pl)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ingested || r.ing.got.UserFileID != "uf-1" || r.ing.got.OwnerUserID != "owner-9" {
		t.Fatalf("ingest input = %+v (called=%v)", r.ing.got, r.ingested)
	}
	if links.calls != 1 {
		t.Errorf("link lookups = %d, want 1", links.calls)
	}
	if r.proc.fpCalls != 0 || len(r.store.finds) != 0 || r.idx.copies != 0 {
		t.Errorf("re-embed attempted copy mode: fpCalls=%d finds=%d copies=%d", r.proc.fpCalls, len(r.store.finds), r.idx.copies)
	}
	if addCount("ingest") != before {
		t.Error("a re-embed must not count as an add")
	}
}

// A non-library file resolves to no link: the plain ingest path, unchanged.
func TestFileHandler_ReEmbedNonLibraryUnchanged(t *testing.T) {
	r, err := runLinked(t, jobs.TypeReEmbedding, &fakeLinks{}, jobs.FileProcessingPayload{FileID: "f", KbID: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ingested || r.ing.got.UserFileID != "" || r.ing.got.OwnerUserID != "" {
		t.Fatalf("ingest input = %+v (called=%v)", r.ing.got, r.ingested)
	}
}

// A failed lookup proceeds as a non-library file (no copy, no caches) rather
// than trusting the payload.
func TestFileHandler_LinkLookupErrorProceedsAsNonLibrary(t *testing.T) {
	r, err := runLinked(t, jobs.TypeFileProcessing, &fakeLinks{err: errors.New("db down")}, libPayload())
	if err != nil {
		t.Fatal(err)
	}
	if !r.ingested || r.ing.got.UserFileID != "" || r.ing.got.OwnerUserID != "" || len(r.store.finds) != 0 {
		t.Fatalf("ingest input = %+v finds=%d", r.ing.got, len(r.store.finds))
	}
}

// For an add task the row's link supersedes the payload's, and copy mode runs.
func TestFileHandler_AddTaskUsesRowLinkAndCopies(t *testing.T) {
	pl := libPayload()
	pl.UserFileID = "uf-stale"
	r, err := runLinked(t, jobs.TypeFileProcessing, &fakeLinks{uf: "uf-1", owner: "owner-9"}, pl)
	if err != nil {
		t.Fatal(err)
	}
	if r.ingested || r.store.marks != 1 {
		t.Fatalf("ingested=%v marks=%d; want a copy", r.ingested, r.store.marks)
	}
	if len(r.store.finds) == 0 || r.store.finds[0].userFileID != "uf-1" {
		t.Errorf("FindCopyDonor calls = %+v, want uf-1 from the row", r.store.finds)
	}
	if r.proc.cacheArgs[1] != "owner-9" {
		t.Errorf("owner = %q, want owner-9 from the row", r.proc.cacheArgs[1])
	}
}
