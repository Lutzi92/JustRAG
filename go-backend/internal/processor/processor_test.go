package processor

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// mockStore
// ---------------------------------------------------------------------------

var _ ProcessorStore = (*mockStore)(nil)

type mockStore struct {
	statuses   []string
	progresses []int
}

func (m *mockStore) UpdateFileStatus(_ context.Context, _ string, status string) error {
	m.statuses = append(m.statuses, status)
	return nil
}

func (m *mockStore) UpdateFileProgress(_ context.Context, _ string, progress int) error {
	m.progresses = append(m.progresses, progress)
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

func TestUpdateTerminalStatusUsesLiveContext(t *testing.T) {
	store := &contextCapturingStore{}
	p := NewProcessor(nil, nil, nil, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.store.UpdateFileStatus(ctx, "file-1", "processing"); err != nil {
		t.Fatalf("unexpected error updating processing status: %v", err)
	}
	p.updateTerminalStatus(context.Background(), "file-1", "error")

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
