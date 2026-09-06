package processor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/ingest"
	"github.com/justrag/go-backend/internal/tabular/profile"
	"github.com/justrag/go-backend/internal/vector"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ---------------------------------------------------------------------------
// mockStore
// ---------------------------------------------------------------------------

var _ ProcessorStore = (*mockStore)(nil)

// stageInfo captures the most recent UpdateFileStage call for one file:
// total is constant across a file's whole run (it's the static plan size),
// so tests only need the last-observed value.
type stageInfo struct {
	index, total int
}

type mockStore struct {
	statuses        []string
	progresses      []int
	errStages       []string
	errMsgs         []string
	stages          map[string]stageInfo
	parseReports    map[string][]byte
	lastStageDetail map[string]string

	// Ingest prompt-injection screening (W5-R8). origins is the seeded
	// files.origin per file id (default "upload" for an unseeded id, the
	// production default); the rest record what screenIfExternal did.
	origins          map[string]string
	originCalls      int
	injectionDetails map[string][]byte
	injectionCleared []string
}

func (m *mockStore) UpdateFileStatus(_ context.Context, _ string, status string) error {
	m.statuses = append(m.statuses, status)
	return nil
}

func (m *mockStore) UpdateFileProgress(_ context.Context, _ string, progress int) error {
	m.progresses = append(m.progresses, progress)
	return nil
}

func (m *mockStore) MarkFileError(_ context.Context, _ string, stage, message string) error {
	m.statuses = append(m.statuses, "error")
	m.errStages = append(m.errStages, stage)
	m.errMsgs = append(m.errMsgs, message)
	return nil
}

func (m *mockStore) UpdateFileStage(_ context.Context, fileID, _ string, index, total int) error {
	if m.stages == nil {
		m.stages = make(map[string]stageInfo)
	}
	m.stages[fileID] = stageInfo{index: index, total: total}
	return nil
}

func (m *mockStore) ClearFileStage(context.Context, string) error {
	return nil
}

func (m *mockStore) SetFileParseReport(_ context.Context, fileID string, report []byte) error {
	if m.parseReports == nil {
		m.parseReports = make(map[string][]byte)
	}
	m.parseReports[fileID] = report
	return nil
}

func (m *mockStore) UpdateFileStageDetail(_ context.Context, fileID, detail string) error {
	if m.lastStageDetail == nil {
		m.lastStageDetail = make(map[string]string)
	}
	m.lastStageDetail[fileID] = detail
	return nil
}

func (m *mockStore) GetFileOrigin(_ context.Context, fileID string) (string, error) {
	m.originCalls++
	if o, ok := m.origins[fileID]; ok {
		return o, nil
	}
	return "upload", nil
}

func (m *mockStore) SetInjectionFlag(_ context.Context, fileID string, detail []byte) error {
	if m.injectionDetails == nil {
		m.injectionDetails = make(map[string][]byte)
	}
	m.injectionDetails[fileID] = detail
	return nil
}

func (m *mockStore) ClearInjectionFlag(_ context.Context, fileID string) error {
	m.injectionCleared = append(m.injectionCleared, fileID)
	return nil
}

type contextCapturingStore struct {
	statusCtxErrs []error
}

func (s *contextCapturingStore) UpdateFileStatus(ctx context.Context, _ string, _ string) error {
	s.statusCtxErrs = append(s.statusCtxErrs, ctx.Err())
	return nil
}

func (s *contextCapturingStore) UpdateFileProgress(context.Context, string, int) error {
	return nil
}

func (s *contextCapturingStore) MarkFileError(ctx context.Context, _, _, _ string) error {
	s.statusCtxErrs = append(s.statusCtxErrs, ctx.Err())
	return nil
}

func (s *contextCapturingStore) UpdateFileStage(context.Context, string, string, int, int) error {
	return nil
}

func (s *contextCapturingStore) ClearFileStage(context.Context, string) error {
	return nil
}

func (s *contextCapturingStore) SetFileParseReport(context.Context, string, []byte) error {
	return nil
}

func (s *contextCapturingStore) UpdateFileStageDetail(context.Context, string, string) error {
	return nil
}

func (s *contextCapturingStore) GetFileOrigin(context.Context, string) (string, error) {
	return "upload", nil
}

func (s *contextCapturingStore) SetInjectionFlag(context.Context, string, []byte) error {
	return nil
}

func (s *contextCapturingStore) ClearInjectionFlag(context.Context, string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestNewProcessor verifies that NewProcessor returns a non-nil *Processor.
func TestNewProcessor(t *testing.T) {
	p := NewProcessor(nil, nil, nil, &mockStore{})
	if p == nil {
		t.Fatal("NewProcessor returned nil")
	}
}

// TestResolveKBLanguage_NoMainDB verifies that without a mainDB pool the
// processor degrades to the "simple" Postgres regconfig instead of failing.
// The worker wires mainDB explicitly; tests and the in-process eval CLI
// don't, and they shouldn't be forced to.
func TestResolveKBLanguage_NoMainDB(t *testing.T) {
	p := NewProcessor(nil, nil, nil, &mockStore{})
	got := p.resolveKBLanguage(context.Background(), "any-kb")
	if got != "simple" {
		t.Errorf("expected 'simple' fallback, got %q", got)
	}
}

// TestBatches verifies that 5 items with batch size 2 produces [2, 2, 1].
func TestBatches(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	got := batches(items, 2)

	if len(got) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(got))
	}
	if len(got[0]) != 2 {
		t.Errorf("batch 0: expected 2 items, got %d", len(got[0]))
	}
	if len(got[1]) != 2 {
		t.Errorf("batch 1: expected 2 items, got %d", len(got[1]))
	}
	if len(got[2]) != 1 {
		t.Errorf("batch 2 (remainder): expected 1 item, got %d", len(got[2]))
	}
}

// TestBatchesExact verifies evenly-divisible input produces no remainder batch.
func TestBatchesExact(t *testing.T) {
	items := []int{1, 2, 3, 4}
	got := batches(items, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(got))
	}
}

// TestBatchesEmpty verifies that an empty input slice returns nil.
func TestBatchesEmpty(t *testing.T) {
	got := batches([]string{}, 5)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

// TestBatchesSingleLargerThanInput verifies that a batch size larger than the
// slice returns a single batch containing all items.
func TestBatchesSingleLargerThanInput(t *testing.T) {
	items := []int{1, 2, 3}
	got := batches(items, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(got))
	}
	if len(got[0]) != 3 {
		t.Errorf("expected 3 items in single batch, got %d", len(got[0]))
	}
}

// TestProcessFileNoParser verifies that ProcessFile sets status to "error" and
// returns an error when no parser is available for the given MIME type.
func TestProcessFileNoParser(t *testing.T) {
	store := &mockStore{}
	// An empty factory (no registered parsers) cannot match any file.
	factory := parser.NewFactory()
	p := NewProcessor(factory, nil, nil, store)

	err := p.ProcessFile(context.Background(), ProcessFileInput{
		FileID:   "file-1",
		FilePath: "/tmp/x.bin",
		FileName: "unknown.bin",
		MimeType: "application/octet-stream",
		KBID:     "kb-1",
	})
	if err == nil {
		t.Fatal("expected error when no parser available, got nil")
	}

	// Status transitions: "processing" then "error".
	if len(store.statuses) < 2 {
		t.Fatalf("expected at least 2 status updates, got %d: %v", len(store.statuses), store.statuses)
	}
	if store.statuses[0] != "processing" {
		t.Errorf("first status: want %q, got %q", "processing", store.statuses[0])
	}
	if store.statuses[1] != "error" {
		t.Errorf("second status: want %q, got %q", "error", store.statuses[1])
	}
	if len(store.errStages) != 1 || store.errStages[0] != "unsupported_type" {
		t.Errorf("error stage: want [unsupported_type], got %v", store.errStages)
	}
	if len(store.errMsgs) != 1 || !strings.Contains(store.errMsgs[0], "application/octet-stream") {
		t.Errorf("error message should name the mime type, got %v", store.errMsgs)
	}
}

// ---------------------------------------------------------------------------
// fakeSiteConfigReader
// ---------------------------------------------------------------------------

type fakeSiteConfigReader struct{ values map[string]*string }

func (f *fakeSiteConfigReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	return f.values[key], nil
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// resolveEnrichmentEnabled tests
// ---------------------------------------------------------------------------

func TestResolveEnrichmentEnabled_DefaultOn(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if !resolveEnrichmentEnabled(context.Background(), r) {
		t.Fatal("expected true when key is absent")
	}
}

func TestResolveEnrichmentEnabled_ExplicitFalse(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"contextual_enrichment": strPtr("false")}}
	if resolveEnrichmentEnabled(context.Background(), r) {
		t.Fatal("expected false when value is \"false\"")
	}
}

func TestResolveEnrichmentEnabled_ExplicitZero(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"contextual_enrichment": strPtr("0")}}
	if resolveEnrichmentEnabled(context.Background(), r) {
		t.Fatal("expected false when value is \"0\"")
	}
}

func TestResolveEnrichmentEnabled_NilReader(t *testing.T) {
	if !resolveEnrichmentEnabled(context.Background(), nil) {
		t.Fatal("expected true when reader is nil")
	}
}

// ---------------------------------------------------------------------------
// resolveEmbeddingBatchSize tests
// ---------------------------------------------------------------------------

func TestResolveEmbeddingBatchSize_Default(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := resolveEmbeddingBatchSize(context.Background(), r); got != 20 {
		t.Fatalf("expected default 20 when key is absent, got %d", got)
	}
	if got := resolveEmbeddingBatchSize(context.Background(), nil); got != 20 {
		t.Fatalf("expected default 20 when reader is nil, got %d", got)
	}
}

func TestResolveEmbeddingBatchSize_FromConfig(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"embedding_batch_size": strPtr(" 50 ")}}
	if got := resolveEmbeddingBatchSize(context.Background(), r); got != 50 {
		t.Fatalf("expected 50, got %d", got)
	}
}

func TestResolveEmbeddingBatchSize_InvalidFallsBack(t *testing.T) {
	for _, bad := range []string{"0", "-3", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{"embedding_batch_size": strPtr(bad)}}
		if got := resolveEmbeddingBatchSize(context.Background(), r); got != 20 {
			t.Errorf("value %q: expected fallback 20, got %d", bad, got)
		}
	}
}

// ---------------------------------------------------------------------------
// resolveEnrichmentModel tests
// ---------------------------------------------------------------------------

func TestResolveEnrichmentModel_FromConfig(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"contextual_enrichment_model": strPtr("fast-model")}}
	got := resolveEnrichmentModel(context.Background(), r)
	if got != "fast-model" {
		t.Fatalf("expected \"fast-model\", got %q", got)
	}
}

func TestResolveEnrichmentModel_EmptyDefault(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	got := resolveEnrichmentModel(context.Background(), r)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestResolveEnrichmentModel_TrimsWhitespace(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"contextual_enrichment_model": strPtr("  fast-model  ")}}
	got := resolveEnrichmentModel(context.Background(), r)
	if got != "fast-model" {
		t.Fatalf("expected \"fast-model\" after trim, got %q", got)
	}
}

// TestResolveEnrichmentModel_TierFallback: when the per-task key
// is unset but model_tier_fast is, the resolver returns the tier
// value. Per-task value still wins when both are set.
func TestResolveEnrichmentModel_TierFallback(t *testing.T) {
	// tier only
	r := &fakeSiteConfigReader{values: map[string]*string{
		"model_tier_fast": strPtr("tier-model"),
	}}
	if got := resolveEnrichmentModel(context.Background(), r); got != "tier-model" {
		t.Errorf("tier fallback failed: got %q, want tier-model", got)
	}

	// per-task wins
	r = &fakeSiteConfigReader{values: map[string]*string{
		"contextual_enrichment_model": strPtr("explicit-model"),
		"model_tier_fast":             strPtr("tier-model"),
	}}
	if got := resolveEnrichmentModel(context.Background(), r); got != "explicit-model" {
		t.Errorf("per-task should win: got %q, want explicit-model", got)
	}
}

// TestResolveKGExtractionModel_TierFallback: same chain on the KG
// extractor's per-task key. Pinned because the resolver lives in
// processor (not chat/siteconfig) so the chain is implemented
// twice — this test is the contract that they stay aligned.
func TestResolveKGExtractionModel_TierFallback(t *testing.T) {
	// tier only
	r := &fakeSiteConfigReader{values: map[string]*string{
		"model_tier_fast": strPtr("tier-model"),
	}}
	if got := resolveKGExtractionModel(context.Background(), r); got != "tier-model" {
		t.Errorf("tier fallback failed: got %q, want tier-model", got)
	}

	// per-task wins
	r = &fakeSiteConfigReader{values: map[string]*string{
		"kg_extraction_model": strPtr("explicit-model"),
		"model_tier_fast":     strPtr("tier-model"),
	}}
	if got := resolveKGExtractionModel(context.Background(), r); got != "explicit-model" {
		t.Errorf("per-task should win: got %q, want explicit-model", got)
	}

	// neither → empty
	r = &fakeSiteConfigReader{values: map[string]*string{}}
	if got := resolveKGExtractionModel(context.Background(), r); got != "" {
		t.Errorf("neither set: got %q, want empty", got)
	}
}

func TestMarkTerminalErrorUsesLiveContext(t *testing.T) {
	store := &contextCapturingStore{}
	p := NewProcessor(nil, nil, nil, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.store.UpdateFileStatus(ctx, "file-1", "processing"); err != nil {
		t.Fatalf("unexpected error updating processing status: %v", err)
	}
	// markTerminalError must detach cancellation via context.WithoutCancel —
	// passing the cancelled ctx makes the test fail if that detachment is ever removed.
	p.markTerminalError(ctx, "file-1", "canceled", "Processing was interrupted")

	if len(store.statusCtxErrs) != 2 {
		t.Fatalf("expected 2 status calls, got %d", len(store.statusCtxErrs))
	}
	if store.statusCtxErrs[0] == nil {
		t.Fatal("expected cancelled context on first status update")
	}
	if store.statusCtxErrs[1] != nil {
		t.Fatalf("expected live context on terminal status update, got %v", store.statusCtxErrs[1])
	}
}

// ---------------------------------------------------------------------------
// fakeHashLookup
// ---------------------------------------------------------------------------

type fakeHashLookup struct {
	existing map[string]struct{}
	calls    int
}

func (f *fakeHashLookup) GetExistingChunkHashes(_ context.Context, _ string, _ int, hashes []string) (map[string]struct{}, error) {
	f.calls++
	out := make(map[string]struct{})
	for _, h := range hashes {
		if _, ok := f.existing[h]; ok {
			out[h] = struct{}{}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// dedupBatch tests
// ---------------------------------------------------------------------------

func TestDedupBatch_InBatchDuplicates(t *testing.T) {
	res, err := dedupBatch(context.Background(), nil, "kb", 1536, []string{
		"Hello World",
		"Hello world",
		"Different content",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(res.survivorIdx) != 2 {
		t.Errorf("expected 2 survivors, got %d", len(res.survivorIdx))
	}
	if res.droppedCount != 1 {
		t.Errorf("expected 1 dropped, got %d", res.droppedCount)
	}
}

func TestDedupBatch_CrossFileDuplicates(t *testing.T) {
	existingHash := vector.HashContent("Existing Chunk")
	lookup := &fakeHashLookup{existing: map[string]struct{}{existingHash: {}}}
	res, err := dedupBatch(context.Background(), lookup, "kb", 1536, []string{
		"New chunk",
		"Existing chunk",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(res.survivorIdx) != 1 {
		t.Errorf("expected 1 survivor (cross-file dup filtered), got %d", len(res.survivorIdx))
	}
	if res.droppedCount != 1 {
		t.Errorf("expected 1 dropped, got %d", res.droppedCount)
	}
	if lookup.calls != 1 {
		t.Errorf("expected 1 lookup call, got %d", lookup.calls)
	}
}

func TestDedupBatch_EmptyTextsNotDeduped(t *testing.T) {
	res, _ := dedupBatch(context.Background(), nil, "kb", 1536, []string{
		"",
		"  ",
		"\n\t",
	})
	if len(res.survivorIdx) != 3 {
		t.Errorf("expected all 3 empties kept, got %d survivors", len(res.survivorIdx))
	}
}

func TestDedupBatch_NilLookupOnlyInBatch(t *testing.T) {
	res, _ := dedupBatch(context.Background(), nil, "kb", 1536, []string{"A", "B", "A"})
	if len(res.survivorIdx) != 2 {
		t.Errorf("expected 2 survivors with nil lookup, got %d", len(res.survivorIdx))
	}
}

func TestDedupBatch_AllNew(t *testing.T) {
	lookup := &fakeHashLookup{existing: map[string]struct{}{}}
	res, _ := dedupBatch(context.Background(), lookup, "kb", 1536, []string{"x", "y", "z"})
	if len(res.survivorIdx) != 3 {
		t.Errorf("expected 3 survivors, got %d", len(res.survivorIdx))
	}
	if res.droppedCount != 0 {
		t.Errorf("expected 0 dropped, got %d", res.droppedCount)
	}
}

// ---------------------------------------------------------------------------
// reingestCleaner seam test
// ---------------------------------------------------------------------------

// spyCleaner records calls to the two cleanup methods so
// TestProcessFile_DeletesChunksBeforeIngest can verify they fired.
type spyCleaner struct {
	deletedChunks  []string
	deletedParents []string
}

func (s *spyCleaner) DeleteChunksByFileIDAllDims(_ context.Context, fileID string) error {
	s.deletedChunks = append(s.deletedChunks, fileID)
	return nil
}

func (s *spyCleaner) DeleteParentChunksByFileID(_ context.Context, fileID string) error {
	s.deletedParents = append(s.deletedParents, fileID)
	return nil
}

// TestProcessFile_DeletesChunksBeforeIngest verifies that ProcessFile calls
// both cleanup methods BEFORE the no-parser early-return, so an Asynq retry
// of a partially-failed attempt replaces rather than duplicates chunks.
//
// The test is deliberately DB-free: the empty parser.Factory means ProcessFile
// exits at "Step 2: select parser" — right after the cleanup block — so we
// never need a live vector store.
func TestProcessFile_DeletesChunksBeforeIngest(t *testing.T) {
	spy := &spyCleaner{}
	store := &mockStore{}
	factory := parser.NewFactory() // empty — no parser matches

	p := NewProcessor(factory, nil, nil, store)
	p.cleaner = spy // inject seam

	// We expect an error (no parser), but cleanup must have fired first.
	_ = p.ProcessFile(context.Background(), ProcessFileInput{
		FileID:   "file-1",
		FilePath: "/tmp/x.bin",
		FileName: "unknown.bin",
		MimeType: "application/octet-stream",
		KBID:     "kb-1",
	})

	if len(spy.deletedChunks) != 1 || spy.deletedChunks[0] != "file-1" {
		t.Errorf("DeleteChunksByFileIDAllDims: want [file-1], got %v", spy.deletedChunks)
	}
	if len(spy.deletedParents) != 1 || spy.deletedParents[0] != "file-1" {
		t.Errorf("DeleteParentChunksByFileID: want [file-1], got %v", spy.deletedParents)
	}
}

// ---------------------------------------------------------------------------
// kgDeleter seam test
// ---------------------------------------------------------------------------

// spyKGDeleter records DeleteKGForFile calls and can simulate a failure so
// TestClearStaleKG_* can verify best-effort behaviour.
type spyKGDeleter struct {
	calls   [][2]string // {kbID, fileID}
	failErr error
}

func (s *spyKGDeleter) DeleteKGForFile(_ context.Context, kbID, fileID string) error {
	s.calls = append(s.calls, [2]string{kbID, fileID})
	return s.failErr
}

func TestClearStaleKG_CallsDeleterWithKBAndFile(t *testing.T) {
	spy := &spyKGDeleter{}
	p := NewProcessor(parser.NewFactory(), nil, nil, &mockStore{})
	p.kgCleaner = spy

	p.clearStaleKG(context.Background(), "kb-1", "file-1")

	if len(spy.calls) != 1 || spy.calls[0] != [2]string{"kb-1", "file-1"} {
		t.Errorf("DeleteKGForFile: want one call with {kb-1,file-1}, got %v", spy.calls)
	}
}

func TestClearStaleKG_NilDeleterIsNoOp(t *testing.T) {
	p := NewProcessor(parser.NewFactory(), nil, nil, &mockStore{})
	// p.kgCleaner left nil — must not panic.
	p.clearStaleKG(context.Background(), "kb-1", "file-1")
}

func TestClearStaleKG_ToleratesDeleterError(t *testing.T) {
	spy := &spyKGDeleter{failErr: fmt.Errorf("boom")}
	p := NewProcessor(parser.NewFactory(), nil, nil, &mockStore{})
	p.kgCleaner = spy

	// Must not panic or propagate; best-effort like chunk cleanup.
	p.clearStaleKG(context.Background(), "kb-1", "file-1")

	if len(spy.calls) != 1 {
		t.Errorf("want deleter invoked once even on error, got %d calls", len(spy.calls))
	}
}

// ---------------------------------------------------------------------------
// toParseResult tests
// ---------------------------------------------------------------------------

// TestToParseResult verifies the ingest.Result → parser.ParseResult
// conversion: pages stay 1-based (straight from ingest.Page.Number, no
// off-by-one reindexing), IsMarkdown is always true (a spreadsheet's hybrid
// render is markdown regardless of source format), and Text carries the
// joined text ingest.Result already assembled.
func TestToParseResult(t *testing.T) {
	res := &ingest.Result{
		Text: "page one\n\npage two",
		Pages: []ingest.Page{
			{Number: 1, Text: "page one"},
			{Number: 2, Text: "page two"},
		},
	}

	out := toParseResult(res)

	if !out.IsMarkdown {
		t.Error("IsMarkdown must be true for a spreadsheet's hybrid render")
	}
	if out.Text != "page one\n\npage two" {
		t.Errorf("Text = %q, want the ingest result's joined text", out.Text)
	}
	if len(out.Pages) != 2 {
		t.Fatalf("Pages: got %d, want 2", len(out.Pages))
	}
	if out.Pages[0].PageNumber != 1 || out.Pages[0].Text != "page one" {
		t.Errorf("Pages[0] = %+v, want {1, \"page one\"}", out.Pages[0])
	}
	if out.Pages[1].PageNumber != 2 || out.Pages[1].Text != "page two" {
		t.Errorf("Pages[1] = %+v, want {2, \"page two\"}", out.Pages[1])
	}
}

// TestToParseResult_NoPages verifies the zero-pages edge case doesn't panic
// and leaves Pages nil (buildIndexedChunks then falls back to splitting the
// whole Text as one unpaginated document).
func TestToParseResult_NoPages(t *testing.T) {
	out := toParseResult(&ingest.Result{Text: "solo text"})
	if !out.IsMarkdown || out.Text != "solo text" {
		t.Errorf("got %+v", out)
	}
	if len(out.Pages) != 0 {
		t.Errorf("Pages: got %d, want 0", len(out.Pages))
	}
}

// ---------------------------------------------------------------------------
// SpreadsheetIngester seam test
// ---------------------------------------------------------------------------

// fakeIngester is a plain SpreadsheetIngester fake: it records every Ingest
// call and, per ruling R15, WithLLM records the LLM it received and returns
// itself (WithLLM on the real *ingest.Ingester returns a shallow copy, not
// the interface, so the fake mirrors "record + return something usable").
//
// The result's Text/Pages are deliberately whitespace-only: ProcessFile's
// aiResolver is nil in this test (no embedding provider configured), so a
// non-empty rendered text would reach the real embedding call and panic on
// a nil *ai.ConfigResolver. splitter.Split trims and drops whitespace-only
// input, so buildIndexedChunks yields zero chunks and ProcessFile takes its
// existing "0 chunks → completed" early return right after storing the
// parse report and clearing the stage detail — both of which are set before
// that check, so this still exercises everything under test.
type fakeIngester struct {
	calls []ingest.Input
	llm   profile.LLMProfiler
	// err, when set, makes Ingest return it instead of a result — used to
	// exercise the error path of the ingest-metrics wiring without a real
	// failing spreadsheet.
	err error
}

func (f *fakeIngester) Ingest(_ context.Context, in ingest.Input) (*ingest.Result, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &ingest.Result{
		Text:  "   ",
		Pages: []ingest.Page{{Number: 1, Text: "   "}},
		Report: tabular.ParseReport{
			Version: 1,
			Sheets: []tabular.SheetReport{
				{Name: "S", Kind: "table", RowsRead: 10, RowsMaterialised: 8, RowsEmbedded: 6, RowsPastCap: 2},
			},
		},
	}, nil
}

func (f *fakeIngester) WithLLM(llm profile.LLMProfiler) SpreadsheetIngester {
	f.llm = llm
	return f
}

// TestProcessFile_SpreadsheetUsesIngesterAndSkipsEnrichment verifies that a
// spreadsheet file with an ingester wired routes through it (not the
// factory's SpreadsheetParser), stores the parse report, and that the stage
// plan excludes enrich/kg/hype/raptor even though contextual_enrichment,
// kg_extraction_enabled, hype_enabled, and raptor_enabled are ALL explicitly
// on in site_config — isSpreadsheet must force every one of them off via
// spreadsheetStageFlags. Also asserts the ingester was called with a nil LLM
// profiler (no AI resolver wired in this test), and that a successful
// Ingest call records the "ok" outcome metric plus the per-sheet row-kind
// counters (Task 3).
func TestProcessFile_SpreadsheetUsesIngesterAndSkipsEnrichment(t *testing.T) {
	store := &mockStore{}
	p := NewProcessor(parser.DefaultFactoryWith(nil), nil, nil, store)
	p.SetSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
		"contextual_enrichment":      strPtr("true"),
		"kg_extraction_enabled":      strPtr("true"),
		"hype_enabled":               strPtr("true"),
		"raptor_enabled":             strPtr("true"),
	}})
	ing := &fakeIngester{}
	p.SetIngester(ing)

	beforeOK := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("ok"))
	beforeErr := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("error"))
	beforeRead := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("read"))
	beforeMaterialised := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("materialised"))
	beforeEmbedded := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("embedded"))
	beforePastCap := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("past_cap"))

	_ = p.ProcessFile(context.Background(), ProcessFileInput{
		FileID:    "f1",
		FilePath:  "../sheetsource/testdata/ids_leading_zero.xlsx",
		FileName:  "ids_leading_zero.xlsx",
		MimeType:  "",
		KBID:      "kb",
		ChunkSize: 512,
	})

	if len(ing.calls) != 1 || !ing.calls[0].Options.Materialize {
		t.Fatalf("ingester calls: %+v", ing.calls)
	}
	if ing.llm != nil {
		t.Errorf("expected WithLLM(nil) (no AI resolver wired), got %+v", ing.llm)
	}
	if store.parseReports["f1"] == nil || !strings.Contains(string(store.parseReports["f1"]), `"kind":"table"`) {
		t.Errorf("parse report not stored: %s", store.parseReports["f1"])
	}
	if got := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("ok")) - beforeOK; got != 1 {
		t.Errorf("rag_tabular_ingest_total{outcome=ok} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("error")) - beforeErr; got != 0 {
		t.Errorf("rag_tabular_ingest_total{outcome=error} delta = %v, want 0", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("read")) - beforeRead; got != 10 {
		t.Errorf("rows{kind=read} delta = %v, want 10", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("materialised")) - beforeMaterialised; got != 8 {
		t.Errorf("rows{kind=materialised} delta = %v, want 8", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("embedded")) - beforeEmbedded; got != 6 {
		t.Errorf("rows{kind=embedded} delta = %v, want 6", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestRowsForTest().WithLabelValues("past_cap")) - beforePastCap; got != 2 {
		t.Errorf("rows{kind=past_cap} delta = %v, want 2", got)
	}
	// parse + tabular + embed: buildStagePlan always includes parse and
	// embed; tabular is added because materialise is on; enrich/kg/hype/
	// raptor are excluded because isSpreadsheet forces them off even though
	// every one of their own site_config gates is on above. The embed
	// stage is never actually reached in this test (0 chunks → early
	// return before stageEmbed's setStage call), but plan.total() is fixed
	// for the whole plan, so the last stage call recorded (tabular) still
	// carries the full total.
	if store.stages["f1"].total != 3 {
		t.Errorf("stage total = %d, want 3", store.stages["f1"].total)
	}
	if store.lastStageDetail["f1"] != "" {
		t.Errorf("stage detail must be cleared at the end, got %q", store.lastStageDetail["f1"])
	}
}

// TestProcessFile_SpreadsheetIngestFailure_RecordsErrorMetric verifies that
// a failing ingest.Ingester.Ingest call records the "error" outcome (never
// "ok") and no row-kind counters (there is no report to read rows from) —
// the mutation guard for "record ok on the error path".
func TestProcessFile_SpreadsheetIngestFailure_RecordsErrorMetric(t *testing.T) {
	store := &mockStore{}
	p := NewProcessor(parser.DefaultFactoryWith(nil), nil, nil, store)
	p.SetSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{
		"chat_tabular_query_enabled": strPtr("true"),
	}})
	ing := &fakeIngester{err: errors.New("boom")}
	p.SetIngester(ing)

	beforeOK := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("ok"))
	beforeErr := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("error"))

	err := p.ProcessFile(context.Background(), ProcessFileInput{
		FileID:    "f2",
		FilePath:  "../sheetsource/testdata/ids_leading_zero.xlsx",
		FileName:  "ids_leading_zero.xlsx",
		MimeType:  "",
		KBID:      "kb",
		ChunkSize: 512,
	})
	if err == nil {
		t.Fatal("expected ProcessFile to return an error when Ingest fails")
	}

	if got := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("ok")) - beforeOK; got != 0 {
		t.Errorf("rag_tabular_ingest_total{outcome=ok} delta = %v, want 0 on ingest failure", got)
	}
	if got := testutil.ToFloat64(observability.TabularIngestTotalForTest().WithLabelValues("error")) - beforeErr; got != 1 {
		t.Errorf("rag_tabular_ingest_total{outcome=error} delta = %v, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// spreadsheetStageFlags tests
// ---------------------------------------------------------------------------

// TestSpreadsheetStageFlags is the single source of truth's own unit test:
// a spreadsheet forces all four stages off regardless of their inputs; a
// non-spreadsheet passes every input through unchanged.
func TestSpreadsheetStageFlags(t *testing.T) {
	cases := []struct {
		name                                   string
		isSpreadsheet                          bool
		enrich, kg, hype, raptor               bool
		wantEnrich, wantKG, wantHyPE, wantRapt bool
	}{
		{
			name:          "spreadsheet forces all four off even when all four are on",
			isSpreadsheet: true,
			enrich:        true, kg: true, hype: true, raptor: true,
			wantEnrich: false, wantKG: false, wantHyPE: false, wantRapt: false,
		},
		{
			name:          "spreadsheet stays off when all four are already off",
			isSpreadsheet: true,
			enrich:        false, kg: false, hype: false, raptor: false,
			wantEnrich: false, wantKG: false, wantHyPE: false, wantRapt: false,
		},
		{
			name:          "non-spreadsheet passes all four through when on",
			isSpreadsheet: false,
			enrich:        true, kg: true, hype: true, raptor: true,
			wantEnrich: true, wantKG: true, wantHyPE: true, wantRapt: true,
		},
		{
			name:          "non-spreadsheet passes all four through when off",
			isSpreadsheet: false,
			enrich:        false, kg: false, hype: false, raptor: false,
			wantEnrich: false, wantKG: false, wantHyPE: false, wantRapt: false,
		},
		{
			name:          "non-spreadsheet passes a mixed combination through unchanged",
			isSpreadsheet: false,
			enrich:        true, kg: false, hype: true, raptor: false,
			wantEnrich: true, wantKG: false, wantHyPE: true, wantRapt: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotEnrich, gotKG, gotHyPE, gotRapt := spreadsheetStageFlags(tc.isSpreadsheet, tc.enrich, tc.kg, tc.hype, tc.raptor)
			if gotEnrich != tc.wantEnrich || gotKG != tc.wantKG || gotHyPE != tc.wantHyPE || gotRapt != tc.wantRapt {
				t.Errorf("spreadsheetStageFlags(%v, %v, %v, %v, %v) = (%v, %v, %v, %v), want (%v, %v, %v, %v)",
					tc.isSpreadsheet, tc.enrich, tc.kg, tc.hype, tc.raptor,
					gotEnrich, gotKG, gotHyPE, gotRapt,
					tc.wantEnrich, tc.wantKG, tc.wantHyPE, tc.wantRapt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LargeFileGate integration tests (ProcessFile wiring)
// ---------------------------------------------------------------------------
//
// These exercise the gate through the real ProcessFile spreadsheet branch
// (SetLargeFileGate + chat.TabularLargeFileBytes via site_config), as
// opposed to large_file_gate_test.go's tests of *LargeFileGate in
// isolation. tabular_large_file_bytes is clamped to a minimum of 1 MiB
// (chat.TabularLargeFileBytes clamps to [1_048_576, 1_073_741_824] and
// falls back to its 20 MiB default for any out-of-range value) so the
// "large" and "small" fixture files below must straddle that 1 MiB floor,
// not some small round number.

const (
	testLargeFileThresholdBytes = 1_048_576 // the minimum chat.TabularLargeFileBytes allows
	testBigFileBytes            = 2 * 1024 * 1024
	testSmallFileBytes          = 1024
)

// gateTestStore is a mutex-guarded ProcessorStore fake. The concurrency
// tests below run multiple ProcessFile calls against one store from
// separate goroutines; mockStore's plain slices/maps are not safe for
// that (and would trip -race).
type gateTestStore struct {
	mu     sync.Mutex
	detail map[string]string
}

func newGateTestStore() *gateTestStore {
	return &gateTestStore{detail: make(map[string]string)}
}

func (s *gateTestStore) UpdateFileStatus(context.Context, string, string) error { return nil }
func (s *gateTestStore) UpdateFileProgress(context.Context, string, int) error  { return nil }
func (s *gateTestStore) MarkFileError(context.Context, string, string, string) error {
	return nil
}
func (s *gateTestStore) UpdateFileStage(context.Context, string, string, int, int) error {
	return nil
}

// ClearFileStage mirrors the real PGStore, which NULLs stage_detail along
// with the other stage columns at true completion (files.PGStore.ClearFileStage).
func (s *gateTestStore) ClearFileStage(_ context.Context, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.detail, fileID)
	return nil
}
func (s *gateTestStore) SetFileParseReport(context.Context, string, []byte) error { return nil }

// The large-file gate test only ingests spreadsheets, which never reach the
// screening hook — these three exist to satisfy ProcessorStore.
func (s *gateTestStore) GetFileOrigin(context.Context, string) (string, error) {
	return "upload", nil
}
func (s *gateTestStore) SetInjectionFlag(context.Context, string, []byte) error { return nil }
func (s *gateTestStore) ClearInjectionFlag(context.Context, string) error       { return nil }

func (s *gateTestStore) UpdateFileStageDetail(_ context.Context, fileID, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if detail == "" {
		delete(s.detail, fileID)
	} else {
		s.detail[fileID] = detail
	}
	return nil
}

// StageDetail reads the most recently recorded detail for fileID ("" if
// none / cleared). Safe to call concurrently with ProcessFile.
func (s *gateTestStore) StageDetail(fileID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detail[fileID]
}

var _ ProcessorStore = (*gateTestStore)(nil)

// blockingIngester is a SpreadsheetIngester fake that records the file id
// of every Ingest call on started as soon as Ingest is entered, then
// blocks until a value (or a close) arrives on block. Used to prove two
// concurrent Ingest calls are actually serialised by LargeFileGate (as
// opposed to merely not racing on shared state).
type blockingIngester struct {
	started chan string
	block   chan struct{}
}

func (b *blockingIngester) Ingest(_ context.Context, in ingest.Input) (*ingest.Result, error) {
	b.started <- in.FileID
	<-b.block
	// Whitespace-only text/pages: aiResolver is nil in every test using
	// this fake, so a non-empty rendered text would reach the real
	// embedding call and panic on a nil *ai.ConfigResolver. Mirrors
	// fakeIngester's own result above.
	return &ingest.Result{
		Text:   "   ",
		Pages:  []ingest.Page{{Number: 1, Text: "   "}},
		Report: tabular.ParseReport{Version: 1},
	}, nil
}

func (b *blockingIngester) WithLLM(profile.LLMProfiler) SpreadsheetIngester { return b }

// writeTempFile creates a file of exactly size bytes under t.TempDir() and
// returns its path. The ".xlsx" name is what makes
// (&parser.SpreadsheetParser{}).CanParse report true in ProcessFile's
// isSpreadsheet check; the content is never actually parsed in these tests
// (blockingIngester/fakeIngester never read the file), only os.Stat'd by
// the gate.
func writeTempFile(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sheet.xlsx")
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// gateTestProcessor builds a Processor wired with an ingester and a
// large-file gate, with tabular_large_file_bytes fixed at
// testLargeFileThresholdBytes.
func gateTestProcessor(store ProcessorStore, ing SpreadsheetIngester, gate *LargeFileGate) *Processor {
	p := NewProcessor(parser.DefaultFactoryWith(nil), nil, nil, store)
	p.SetSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{
		"tabular_large_file_bytes": strPtr(fmt.Sprintf("%d", testLargeFileThresholdBytes)),
	}})
	p.SetIngester(ing)
	p.SetLargeFileGate(gate)
	return p
}

// TestLargeFileGateSerialisesLargeIngests proves the gate actually
// excludes concurrent Ingest calls for two large spreadsheets, not merely
// that it doesn't crash: the second Ingest must not be entered while the
// first holds the only slot, and it must start only after the first
// releases.
//
// Mutation check: moving the Acquire call to after the Ingest call (i.e.
// gating nothing) makes this test red — both started sends land inside
// the 200ms window and t.Fatalf fires. Verified by hand; see the task
// report for the exact diff and failure output.
func TestLargeFileGateSerialisesLargeIngests(t *testing.T) {
	store := newGateTestStore()
	started := make(chan string, 2)
	block := make(chan struct{})
	ing := &blockingIngester{started: started, block: block}
	gate := NewLargeFileGate(1)
	p := gateTestProcessor(store, ing, gate)

	bigA := writeTempFile(t, testBigFileBytes)
	bigB := writeTempFile(t, testBigFileBytes)

	go func() {
		_ = p.ProcessFile(context.Background(), ProcessFileInput{
			FileID: "big-a", FilePath: bigA, FileName: "a.xlsx", KBID: "kb", ChunkSize: 512,
		})
	}()
	go func() {
		_ = p.ProcessFile(context.Background(), ProcessFileInput{
			FileID: "big-b", FilePath: bigB, FileName: "b.xlsx", KBID: "kb", ChunkSize: 512,
		})
	}()

	first := <-started
	select {
	case second := <-started:
		t.Fatalf("second large ingest %q started while %q held the only slot", second, first)
	case <-time.After(200 * time.Millisecond):
	}

	block <- struct{}{} // release the first, freeing the slot for the second
	second := <-started
	if second == first {
		t.Fatalf("expected the second-starting file to differ from the first, both were %q", first)
	}
	close(block) // release the second
}

// TestSmallFilesBypassTheGate verifies that two files under the size
// threshold both enter Ingest without waiting on each other — the gate
// only ever applies to large files.
func TestSmallFilesBypassTheGate(t *testing.T) {
	store := newGateTestStore()
	started := make(chan string, 2)
	block := make(chan struct{})
	ing := &blockingIngester{started: started, block: block}
	gate := NewLargeFileGate(1)
	p := gateTestProcessor(store, ing, gate)

	smallA := writeTempFile(t, testSmallFileBytes)
	smallB := writeTempFile(t, testSmallFileBytes)

	go func() {
		_ = p.ProcessFile(context.Background(), ProcessFileInput{
			FileID: "small-a", FilePath: smallA, FileName: "a.xlsx", KBID: "kb", ChunkSize: 512,
		})
	}()
	go func() {
		_ = p.ProcessFile(context.Background(), ProcessFileInput{
			FileID: "small-b", FilePath: smallB, FileName: "b.xlsx", KBID: "kb", ChunkSize: 512,
		})
	}()

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatalf("expected both small ingests to start without waiting on the gate; only saw %v", seen)
		}
	}
	close(block) // release both
}

// TestWaitingIsVisibleInStageDetail verifies that while ProcessFile blocks
// on the gate for a large file, the stage detail shows the bilingual
// "waiting for a slot" message (English here — resolveKBLanguages falls
// back to "en" with no mainDB wired), and that it is cleared again once
// the file completes.
//
// Mutation check: deleting the UpdateFileStageDetail(waiting) call before
// Acquire makes this test red (the poll loop below times out because the
// detail is never set) — verified by hand; see the task report.
func TestWaitingIsVisibleInStageDetail(t *testing.T) {
	store := newGateTestStore()
	started := make(chan string, 1)
	block := make(chan struct{})
	ing := &blockingIngester{started: started, block: block}
	gate := NewLargeFileGate(1)
	p := gateTestProcessor(store, ing, gate)

	// Hold the gate's only slot from the test itself so the file below is
	// forced to wait and record the "waiting" stage detail.
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatalf("test acquire: %v", err)
	}

	big := writeTempFile(t, testBigFileBytes)
	done := make(chan struct{})
	go func() {
		_ = p.ProcessFile(context.Background(), ProcessFileInput{
			FileID: "f1", FilePath: big, FileName: "f1.xlsx", KBID: "kb", ChunkSize: 512,
		})
		close(done)
	}()

	const wantWaiting = "Waiting for a large-file slot"
	deadline := time.After(2 * time.Second)
	for {
		if store.StageDetail("f1") == wantWaiting {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("stage detail for f1 never became %q (last seen: %q)", wantWaiting, store.StageDetail("f1"))
		case <-time.After(10 * time.Millisecond):
		}
	}

	gate.Release()      // let ProcessFile's own Acquire succeed
	<-started           // Ingest entered
	block <- struct{}{} // let it finish
	<-done

	if got := store.StageDetail("f1"); got != "" {
		t.Errorf("stage detail after completion = %q, want cleared", got)
	}
}

// ---------------------------------------------------------------------------
// dedupDimensions
// ---------------------------------------------------------------------------

// fakeDedupConfigStore is a minimal ai.ConfigStore that declares a single
// embedding model with a caller-chosen Dimensions value, for exercising
// dedupDimensions' resolver-backed path without a real AI provider.
type fakeDedupConfigStore struct {
	dims int
}

func (f *fakeDedupConfigStore) GetActiveAIProvider(ctx context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "test-provider", Name: "test", APIKey: "test-key", BaseURL: "http://example.invalid"}, nil
}

func (f *fakeDedupConfigStore) GetAIProviderByID(ctx context.Context, id string) (*ai.AIProviderInfo, error) {
	return f.GetActiveAIProvider(ctx)
}

func (f *fakeDedupConfigStore) GetAIModelsByProvider(ctx context.Context, providerID string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{
		{Name: "fake-embed", IsEmbedding: true, Dimensions: f.dims},
	}, nil
}

func (f *fakeDedupConfigStore) GetKBModelOverrides(ctx context.Context, kbID string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

func TestDedupDimensions_FallsBackToLegacyWithoutResolver(t *testing.T) {
	p := &Processor{}
	if got := p.dedupDimensions(context.Background(), "kb"); got != legacyDedupDim {
		t.Fatalf("nil resolver: want %d, got %d", legacyDedupDim, got)
	}
}

func TestDedupDimensions_UsesDeclaredEmbeddingDimension(t *testing.T) {
	resolver := ai.NewConfigResolver(&fakeDedupConfigStore{dims: 4096})
	p := NewProcessor(nil, resolver, nil, &mockStore{})
	if got := p.dedupDimensions(context.Background(), "kb"); got != 4096 {
		t.Fatalf("declared 4096: want 4096, got %d", got)
	}
}
