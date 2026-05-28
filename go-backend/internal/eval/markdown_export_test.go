package eval

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Test fixtures / helpers
// ---------------------------------------------------------------------------

var (
	testRunA = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	testRunB = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	testKBMD = uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
)

func strp(s string) *string { return &s }

func timePtr(t time.Time) *time.Time { return &t }

var epoch = time.Date(2026, 4, 23, 14, 5, 17, 0, time.UTC)

// makeEvalReport builds a Report with route aggregates and per-question metrics.
func makeEvalReport(questions []QuestionReport, routes map[string]AggregateMetrics, agg AggregateMetrics) Report {
	return Report{
		K:               10,
		Questions:       questions,
		Aggregate:       agg,
		RouteAggregates: routes,
	}
}

func marshalReport(t *testing.T, r Report) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshalReport: %v", err)
	}
	return json.RawMessage(b)
}

func marshalSnapshot(t *testing.T, m map[string]*string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshalSnapshot: %v", err)
	}
	return json.RawMessage(b)
}

// makeQuestions builds a slice of QuestionReport entries with given IDs and recalls.
func makeQuestions(ids []string, recalls []float64, qtype string) []QuestionReport {
	if len(ids) != len(recalls) {
		panic("ids and recalls must have same length")
	}
	out := make([]QuestionReport, len(ids))
	for i, id := range ids {
		out[i] = QuestionReport{
			Question: Question{
				ID:        id,
				Question:  "What is the meaning of " + id + "?",
				QueryType: qtype,
			},
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: recalls[i]},
		}
	}
	return out
}

// completedRunPair builds two complete runs with different configs and reports.
func completedRunPair(t *testing.T) (Run, Run) {
	t.Helper()

	snapA := marshalSnapshot(t, map[string]*string{
		"rerank_blend_alpha": strp("0.5"),
		"crag_enabled":       strp("false"),
	})
	snapB := marshalSnapshot(t, map[string]*string{
		"rerank_blend_alpha": strp("0.3"),
		"crag_enabled":       strp("false"),
	})

	questionsA := makeQuestions(
		[]string{"q001", "q002", "q003", "q004", "q005", "q006"},
		[]float64{1.0, 0.5, 0.0, 0.8, 0.3, 0.2},
		"lookup",
	)
	questionsB := makeQuestions(
		[]string{"q001", "q002", "q003", "q004", "q005", "q006"},
		[]float64{1.0, 0.5, 1.0, 0.8, 0.3, 0.0},
		"lookup",
	)

	repA := makeEvalReport(questionsA,
		map[string]AggregateMetrics{
			"lookup": {K: 10, Count: 6, MeanRecall: 0.467, MeanPrecision: 0.1, MRR: 0.3, MeanNDCG: 0.35},
		},
		AggregateMetrics{K: 10, Count: 6, MeanRecall: 0.467, MeanPrecision: 0.1, MRR: 0.3, MeanNDCG: 0.35},
	)
	repB := makeEvalReport(questionsB,
		map[string]AggregateMetrics{
			"lookup": {K: 10, Count: 6, MeanRecall: 0.600, MeanPrecision: 0.12, MRR: 0.35, MeanNDCG: 0.40},
		},
		AggregateMetrics{K: 10, Count: 6, MeanRecall: 0.600, MeanPrecision: 0.12, MRR: 0.35, MeanNDCG: 0.40},
	)

	a := Run{
		ID:             testRunA,
		Status:         "completed",
		Label:          "baseline",
		KBID:           testKBMD,
		FixtureHash:    "abcdef123456789012",
		StartedAt:      timePtr(epoch),
		ConfigSnapshot: snapA,
		Report:         marshalReport(t, repA),
		JudgeEnabled:   false,
		TopK:           10,
	}
	b := Run{
		ID:             testRunB,
		Status:         "completed",
		Label:          "candidate",
		KBID:           testKBMD,
		FixtureHash:    "abcdef123456789012", // same hash → comparable
		StartedAt:      timePtr(epoch.Add(time.Hour)),
		ConfigSnapshot: snapB,
		Report:         marshalReport(t, repB),
		JudgeEnabled:   false,
		TopK:           10,
	}
	return a, b
}

// ---------------------------------------------------------------------------
// Compare tests
// ---------------------------------------------------------------------------

// TestExportCompareMarkdown_BothComplete verifies:
// (a) all three sections present, (b) differing config key appears in diff table,
// (c) lookup route row has correct signed value, (d) overall row is last,
// (e) question-change section has up to 5 entries per side sorted by |delta|.
func TestExportCompareMarkdown_BothComplete(t *testing.T) {
	a, b := completedRunPair(t)
	md := ExportCompareMarkdown(a, b)

	// (a) All three section headers present.
	for _, want := range []string{"## Config diff", "## Aggregate delta (B − A)", "## Notable question changes"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing section: %q\nOutput:\n%s", want, md)
		}
	}

	// (b) differing key appears in config diff table.
	if !strings.Contains(md, "`rerank_blend_alpha`") {
		t.Errorf("expected rerank_blend_alpha in config diff\nOutput:\n%s", md)
	}
	// crag_enabled is the same → must NOT appear in diff.
	if strings.Contains(md, "`crag_enabled`") {
		t.Errorf("crag_enabled (same in both) must not appear in config diff\nOutput:\n%s", md)
	}

	// (c) lookup route row shows correct signed recall delta.
	// repB.lookup.MeanRecall(0.600) - repA.lookup.MeanRecall(0.467) ≈ +0.133
	if !strings.Contains(md, "`lookup`") {
		t.Errorf("lookup route row missing\nOutput:\n%s", md)
	}
	// The recall delta should be +0.133 (formatted as +0.133).
	if !strings.Contains(md, "+0.133") {
		t.Errorf("expected +0.133 recall delta for lookup\nOutput:\n%s", md)
	}

	// (d) overall row is present.
	if !strings.Contains(md, "**overall**") {
		t.Errorf("overall row missing\nOutput:\n%s", md)
	}
	// overall must come after lookup in the aggregate table.
	lookupPos := strings.Index(md, "`lookup`")
	overallPos := strings.Index(md, "**overall**")
	if lookupPos == -1 || overallPos == -1 || overallPos < lookupPos {
		t.Errorf("overall row must come after route rows; lookup=%d overall=%d", lookupPos, overallPos)
	}

	// (e) question-change section has improved and regressed.
	if !strings.Contains(md, "**Improved") {
		t.Errorf("Improved section missing\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "**Regressed") {
		t.Errorf("Regressed section missing\nOutput:\n%s", md)
	}
	// q003: 0.000→1.000 (Δ +1.000) — should be the top improved entry.
	if !strings.Contains(md, "q003") {
		t.Errorf("q003 (biggest improver) missing from output\nOutput:\n%s", md)
	}
	// q006: 0.200→0.000 (Δ -0.200) — should be the top regressed entry.
	if !strings.Contains(md, "q006") {
		t.Errorf("q006 (biggest regressor) missing from output\nOutput:\n%s", md)
	}
}

// TestExportCompareMarkdown_NoConfigDiff verifies that identical snapshots
// yield the "no differences" placeholder.
func TestExportCompareMarkdown_NoConfigDiff(t *testing.T) {
	snap := marshalSnapshot(t, map[string]*string{
		"rerank_blend_alpha": strp("0.5"),
	})
	a := Run{ID: testRunA, KBID: testKBMD, FixtureHash: "hash1", ConfigSnapshot: snap, Status: "completed"}
	b := Run{ID: testRunB, KBID: testKBMD, FixtureHash: "hash1", ConfigSnapshot: snap, Status: "completed"}
	// Give both an empty-but-valid report so the function doesn't fail on parsing.
	emptyRep, _ := json.Marshal(Report{K: 10})
	a.Report = json.RawMessage(emptyRep)
	b.Report = json.RawMessage(emptyRep)

	md := ExportCompareMarkdown(a, b)

	if !strings.Contains(md, "*(no retrieval-relevant config differences)*") {
		t.Errorf("expected no-diff placeholder\nOutput:\n%s", md)
	}
}

// TestExportCompareMarkdown_FixtureMismatch verifies that different fixture
// hashes produce the "NO" comparable line.
func TestExportCompareMarkdown_FixtureMismatch(t *testing.T) {
	a := Run{ID: testRunA, KBID: testKBMD, FixtureHash: "hash-aaa", Status: "queued"}
	b := Run{ID: testRunB, KBID: testKBMD, FixtureHash: "hash-bbb", Status: "queued"}

	md := ExportCompareMarkdown(a, b)

	if !strings.Contains(md, "NO — fixture hashes differ") {
		t.Errorf("expected comparable NO line\nOutput:\n%s", md)
	}
}

// TestExportCompareMarkdown_OneIncomplete verifies that when run B has no
// report, the function does not panic and explains B hasn't completed.
func TestExportCompareMarkdown_OneIncomplete(t *testing.T) {
	a, _ := completedRunPair(t)
	b := Run{
		ID:          testRunB,
		KBID:        testKBMD,
		FixtureHash: "abcdef123456789012",
		Status:      "queued",
		// Report is nil/empty — not completed.
	}

	var md string
	// Must not panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ExportCompareMarkdown panicked: %v", r)
			}
		}()
		md = ExportCompareMarkdown(a, b)
	}()

	// Should mention that B has not completed.
	if !strings.Contains(md, "Run B has not completed") {
		t.Errorf("expected explanation that B hasn't completed\nOutput:\n%s", md)
	}
	// Must not have aggregate table header.
	if strings.Contains(md, "| route | n_B |") {
		t.Errorf("aggregate table must not be present when B is incomplete\nOutput:\n%s", md)
	}
}

// TestExportCompareMarkdown_NoNotableChanges verifies that when all per-question
// deltas are zero, the "no notable changes" placeholder appears.
func TestExportCompareMarkdown_NoNotableChanges(t *testing.T) {
	questions := makeQuestions(
		[]string{"q001", "q002"},
		[]float64{0.5, 0.5},
		"lookup",
	)
	snap := marshalSnapshot(t, map[string]*string{"mmr_lambda": strp("0.7")})
	rep := makeEvalReport(questions,
		map[string]AggregateMetrics{"lookup": {K: 10, Count: 2, MeanRecall: 0.5}},
		AggregateMetrics{K: 10, Count: 2, MeanRecall: 0.5},
	)
	repJSON := marshalReport(t, rep)

	a := Run{ID: testRunA, KBID: testKBMD, FixtureHash: "samehash", ConfigSnapshot: snap, Report: repJSON, Status: "completed"}
	b := Run{ID: testRunB, KBID: testKBMD, FixtureHash: "samehash", ConfigSnapshot: snap, Report: repJSON, Status: "completed"}

	md := ExportCompareMarkdown(a, b)

	// Both improved and regressed sections should show the no-changes placeholder.
	count := strings.Count(md, "*(no notable changes)*")
	if count < 2 {
		t.Errorf("expected 2 occurrences of no-notable-changes placeholder, got %d\nOutput:\n%s", count, md)
	}
}

// TestExportCompareMarkdown_Deterministic verifies that calling the function
// twice on the same inputs produces byte-identical output.
func TestExportCompareMarkdown_Deterministic(t *testing.T) {
	a, b := completedRunPair(t)
	md1 := ExportCompareMarkdown(a, b)
	md2 := ExportCompareMarkdown(a, b)
	if md1 != md2 {
		t.Errorf("ExportCompareMarkdown is not deterministic\nFirst:\n%s\nSecond:\n%s", md1, md2)
	}
}

// ---------------------------------------------------------------------------
// Single-run tests
// ---------------------------------------------------------------------------

// TestExportSingleRunMarkdown_Complete verifies that a full run produces all
// expected sections and that top/bottom 5 lists are populated.
func TestExportSingleRunMarkdown_Complete(t *testing.T) {
	questions := makeQuestions(
		[]string{"q001", "q002", "q003", "q004", "q005", "q006"},
		[]float64{1.0, 0.8, 0.6, 0.4, 0.2, 0.0},
		"enumeration",
	)
	snap := marshalSnapshot(t, map[string]*string{
		"mmr_lambda":         strp("0.7"),
		"rerank_blend_alpha": strp("0.5"),
	})
	rep := makeEvalReport(questions,
		map[string]AggregateMetrics{
			"enumeration": {K: 10, Count: 6, MeanRecall: 0.5, MeanPrecision: 0.1, MRR: 0.3, MeanNDCG: 0.35},
		},
		AggregateMetrics{K: 10, Count: 6, MeanRecall: 0.5, MeanPrecision: 0.1, MRR: 0.3, MeanNDCG: 0.35},
	)

	run := Run{
		ID:             testRunA,
		Status:         "completed",
		Label:          "my-run",
		KBID:           testKBMD,
		FixtureHash:    "abcdef123456789012",
		StartedAt:      timePtr(epoch),
		FinishedAt:     timePtr(epoch.Add(2 * time.Minute)),
		ConfigSnapshot: snap,
		Report:         marshalReport(t, rep),
		JudgeEnabled:   true,
		TopK:           10,
	}

	md := ExportSingleRunMarkdown(run)

	// Header and metadata.
	if !strings.Contains(md, "# Eval run: my-run") {
		t.Errorf("missing title\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "**Judge enabled:** ✓") {
		t.Errorf("missing judge enabled checkmark\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "**Fixture hash:** `abcdef123456`") {
		t.Errorf("missing fixture hash prefix\nOutput:\n%s", md)
	}

	// Config snapshot section.
	if !strings.Contains(md, "## Config snapshot") {
		t.Errorf("missing config snapshot section\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "`mmr_lambda`") {
		t.Errorf("missing mmr_lambda in config snapshot\nOutput:\n%s", md)
	}
	// Keys must be alphabetically sorted: mmr_lambda before rerank_blend_alpha.
	mPos := strings.Index(md, "`mmr_lambda`")
	rPos := strings.Index(md, "`rerank_blend_alpha`")
	if mPos == -1 || rPos == -1 || mPos > rPos {
		t.Errorf("config keys not alphabetically sorted: mmr=%d rerank=%d", mPos, rPos)
	}

	// Aggregate section.
	if !strings.Contains(md, "## Aggregate (k=10)") {
		t.Errorf("missing aggregate section\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "`enumeration`") {
		t.Errorf("missing enumeration route in aggregate\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "**overall**") {
		t.Errorf("missing overall row\nOutput:\n%s", md)
	}
	// overall must come after enumeration.
	enumPos := strings.Index(md, "`enumeration`")
	overallPos := strings.Index(md, "**overall**")
	if enumPos == -1 || overallPos == -1 || overallPos < enumPos {
		t.Errorf("overall must come after route rows; enum=%d overall=%d", enumPos, overallPos)
	}

	// Top / bottom 5.
	if !strings.Contains(md, "## Top 5 questions") {
		t.Errorf("missing top 5 section\nOutput:\n%s", md)
	}
	if !strings.Contains(md, "## Bottom 5 questions") {
		t.Errorf("missing bottom 5 section\nOutput:\n%s", md)
	}
	// q001 (recall 1.0) must appear in top.
	if !strings.Contains(md, "q001") {
		t.Errorf("q001 (recall=1.0) must appear in top questions\nOutput:\n%s", md)
	}
	// q006 (recall 0.0) must appear in bottom.
	if !strings.Contains(md, "q006") {
		t.Errorf("q006 (recall=0.0) must appear in bottom questions\nOutput:\n%s", md)
	}

	// Footer.
	if !strings.Contains(md, "*Generated by JustRAG admin eval.*") {
		t.Errorf("missing footer\nOutput:\n%s", md)
	}

	// Trailing newline.
	if !strings.HasSuffix(md, "\n") {
		t.Errorf("output must end with newline")
	}
}

// TestExportSingleRunMarkdown_Incomplete verifies that a queued run (no report)
// produces a header + status section and no aggregate section; must not panic.
func TestExportSingleRunMarkdown_Incomplete(t *testing.T) {
	run := Run{
		ID:     testRunA,
		Status: "queued",
		KBID:   testKBMD,
		// No Report, no StartedAt.
	}

	var md string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ExportSingleRunMarkdown panicked: %v", r)
			}
		}()
		md = ExportSingleRunMarkdown(run)
	}()

	// Must have a heading.
	if !strings.Contains(md, "# Eval run:") {
		t.Errorf("missing heading\nOutput:\n%s", md)
	}
	// Must have the "not completed" explanation.
	if !strings.Contains(md, "Run has not completed") {
		t.Errorf("expected not-completed explanation\nOutput:\n%s", md)
	}
	// Must NOT have aggregate table header.
	if strings.Contains(md, "## Aggregate") {
		t.Errorf("aggregate section must not appear for incomplete run\nOutput:\n%s", md)
	}
	// Must NOT have top/bottom question lists.
	if strings.Contains(md, "## Top 5") {
		t.Errorf("top 5 section must not appear for incomplete run\nOutput:\n%s", md)
	}
	// Trailing newline.
	if !strings.HasSuffix(md, "\n") {
		t.Errorf("output must end with newline")
	}
}

// ---------------------------------------------------------------------------
// Additional edge-case tests
// ---------------------------------------------------------------------------

// TestExportCompareMarkdown_ComparableYes verifies that matching fixture hashes
// produce the "yes" comparable line.
func TestExportCompareMarkdown_ComparableYes(t *testing.T) {
	a := Run{ID: testRunA, KBID: testKBMD, FixtureHash: "samehash", Status: "queued"}
	b := Run{ID: testRunB, KBID: testKBMD, FixtureHash: "samehash", Status: "queued"}

	md := ExportCompareMarkdown(a, b)
	if !strings.Contains(md, "**Comparable:** yes") {
		t.Errorf("expected comparable yes\nOutput:\n%s", md)
	}
}

// TestExportCompareMarkdown_ConfigDiffSorted verifies that the config diff
// table rows are alphabetically sorted by key.
func TestExportCompareMarkdown_ConfigDiffSorted(t *testing.T) {
	snapA := marshalSnapshot(t, map[string]*string{
		"zzz_key": strp("a"),
		"aaa_key": strp("x"),
	})
	snapB := marshalSnapshot(t, map[string]*string{
		"zzz_key": strp("b"),
		"aaa_key": strp("y"),
	})
	a := Run{ID: testRunA, KBID: testKBMD, FixtureHash: "h", ConfigSnapshot: snapA, Status: "queued"}
	b := Run{ID: testRunB, KBID: testKBMD, FixtureHash: "h", ConfigSnapshot: snapB, Status: "queued"}

	md := ExportCompareMarkdown(a, b)

	aaaPos := strings.Index(md, "`aaa_key`")
	zzzPos := strings.Index(md, "`zzz_key`")
	if aaaPos == -1 || zzzPos == -1 {
		t.Fatalf("both keys must appear in output\nOutput:\n%s", md)
	}
	if aaaPos > zzzPos {
		t.Errorf("aaa_key must appear before zzz_key (alphabetical order)\naaa=%d zzz=%d\nOutput:\n%s",
			aaaPos, zzzPos, md)
	}
}

// TestExportSingleRunMarkdown_NilStarted verifies "(not started)" when StartedAt is nil.
func TestExportSingleRunMarkdown_NilStarted(t *testing.T) {
	run := Run{
		ID:     testRunA,
		Status: "queued",
		KBID:   testKBMD,
		// StartedAt nil
	}
	md := ExportSingleRunMarkdown(run)
	if !strings.Contains(md, "(not started)") {
		t.Errorf("expected '(not started)' when StartedAt is nil\nOutput:\n%s", md)
	}
}

// TestTruncateQuestion verifies truncation and stripping behaviour.
func TestTruncateQuestion(t *testing.T) {
	// 65 runes → truncated to 60 + ellipsis.
	long := strings.Repeat("a", 65)
	got := truncateQuestion(long)
	runes := []rune(got)
	// The ellipsis character counts as 1 rune; total = 61.
	if len(runes) != 61 {
		t.Errorf("truncated length = %d, want 61 (60 + ellipsis)", len(runes))
	}

	// Stripping * and backtick.
	got2 := truncateQuestion("hello `world` *test*")
	if strings.Contains(got2, "`") || strings.Contains(got2, "*") {
		t.Errorf("stripping failed, got: %q", got2)
	}

	// Exactly 60 runes → no ellipsis.
	exact := strings.Repeat("b", 60)
	got3 := truncateQuestion(exact)
	if got3 != exact {
		t.Errorf("60-rune string should not be truncated, got: %q", got3)
	}
}

// TestHashPrefix verifies the first-12-char behaviour.
func TestHashPrefix(t *testing.T) {
	h := "abcdef1234567890"
	if got := hashPrefix(h); got != "abcdef123456" {
		t.Errorf("hashPrefix = %q, want %q", got, "abcdef123456")
	}
	if got := hashPrefix(""); got != "(none)" {
		t.Errorf("hashPrefix('') = %q, want '(none)'", got)
	}
	short := "abc"
	if got := hashPrefix(short); got != short {
		t.Errorf("hashPrefix of short string = %q, want %q", got, short)
	}
}

func TestExportSingleRunMarkdown_IncludesOrchestratorTableWhenPresent(t *testing.T) {
	questions := makeQuestions([]string{"q001"}, []float64{1.0}, "complex_reasoning")
	rep := makeEvalReport(questions,
		map[string]AggregateMetrics{
			"complex_reasoning": {K: 10, Count: 1, MeanRecall: 1.0, MRR: 1.0, MeanNDCG: 1.0},
		},
		AggregateMetrics{K: 10, Count: 1, MeanRecall: 1.0, MRR: 1.0, MeanNDCG: 1.0},
	)
	rep.OrchestratorAggregates = map[string]AggregateMetrics{
		OrchestratorSupervisor: {Count: 1, MeanRecall: 1.0, MRR: 1.0, MeanNDCG: 1.0},
	}
	run := Run{
		ID:           testRunA,
		Status:       "completed",
		KBID:         testKBMD,
		FixtureHash:  "abcdef123456789012",
		StartedAt:    timePtr(epoch),
		FinishedAt:   timePtr(epoch.Add(2 * time.Minute)),
		Report:       marshalReport(t, rep),
		JudgeEnabled: false,
		TopK:         10,
	}

	md := ExportSingleRunMarkdown(run)
	if !strings.Contains(md, "## Per orchestrator") {
		t.Fatalf("missing 'Per orchestrator' section:\n%s", md)
	}
	if !strings.Contains(md, "`"+OrchestratorSupervisor+"`") {
		t.Fatalf("orchestrator name missing from table:\n%s", md)
	}
}

func TestExportSingleRunMarkdown_OmitsOrchestratorTableWhenAbsent(t *testing.T) {
	questions := makeQuestions([]string{"q001"}, []float64{1.0}, "complex_reasoning")
	rep := makeEvalReport(questions, nil, AggregateMetrics{K: 10, Count: 1, MeanRecall: 1.0})
	// OrchestratorAggregates intentionally nil.
	run := Run{
		ID:          testRunA,
		Status:      "completed",
		KBID:        testKBMD,
		FixtureHash: "abcdef123456789012",
		StartedAt:   timePtr(epoch),
		FinishedAt:  timePtr(epoch.Add(2 * time.Minute)),
		Report:      marshalReport(t, rep),
		TopK:        10,
	}
	md := ExportSingleRunMarkdown(run)
	if strings.Contains(md, "## Per orchestrator") {
		t.Fatalf("'Per orchestrator' must be absent when OrchestratorAggregates is nil:\n%s", md)
	}
}
