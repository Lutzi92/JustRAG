package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTempGolden(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "golden.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempGolden: %v", err)
	}
	return path
}

func TestLoadGoldenSet_ParsesValidEntries(t *testing.T) {
	content := `{"id":"q1","question":"What is X?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}
{"id":"q2","question":"Was ist Y?","kb_id":"kb-2","language":"de","must_cite_file_ids":["f2","f3"]}
`
	path := writeTempGolden(t, content)
	qs, err := LoadGoldenSet(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(qs))
	}
	// Entry 2: language "de" + 2-element must_cite_file_ids
	q2 := qs[1]
	if q2.Language != "de" {
		t.Errorf("expected language 'de', got %q", q2.Language)
	}
	if len(q2.MustCiteFileIDs) != 2 {
		t.Fatalf("expected 2 must_cite_file_ids, got %d", len(q2.MustCiteFileIDs))
	}
	if q2.MustCiteFileIDs[0] != "f2" || q2.MustCiteFileIDs[1] != "f3" {
		t.Errorf("unexpected must_cite_file_ids: %v", q2.MustCiteFileIDs)
	}
}

func TestLoadGoldenSet_IgnoresBlankAndCommentLines(t *testing.T) {
	content := `# this is a comment

{"id":"q1","question":"What?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}

`
	path := writeTempGolden(t, content)
	qs, err := LoadGoldenSet(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
}

func TestLoadGoldenSet_RejectsInvalidJSON(t *testing.T) {
	content := `not valid json`
	path := writeTempGolden(t, content)
	_, err := LoadGoldenSet(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("expected error to contain 'line 1', got: %v", err)
	}
}

func TestLoadGoldenSet_RejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "empty id",
			content: `{"id":"","question":"What?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}`,
		},
		{
			name:    "empty question",
			content: `{"id":"q1","question":"","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}`,
		},
		{
			name:    "empty kb_id",
			content: `{"id":"q1","question":"What?","kb_id":"","language":"en","must_cite_file_ids":["f1"]}`,
		},
		{
			name:    "empty must_cite_file_ids",
			content: `{"id":"q1","question":"What?","kb_id":"kb-1","language":"en","must_cite_file_ids":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempGolden(t, tc.content)
			if _, err := LoadGoldenSet(path); err == nil {
				t.Fatalf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}

func TestLoadGoldenSet_RejectsInvalidLanguage(t *testing.T) {
	content := `{"id":"q1","question":"Quoi?","kb_id":"kb-1","language":"fr","must_cite_file_ids":["f1"]}`
	path := writeTempGolden(t, content)
	_, err := LoadGoldenSet(path)
	if err == nil {
		t.Fatal("expected error for invalid language 'fr', got nil")
	}
}

func TestLoadGoldenSet_FileNotFound(t *testing.T) {
	_, err := LoadGoldenSet("/nonexistent/path/to/golden.jsonl")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestLoadGoldenSet_AcceptsQueryType(t *testing.T) {
	content := `{"id":"q1","question":"What is X?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"query_type":"lookup"}
{"id":"q2","question":"Welche?","kb_id":"kb-1","language":"de","must_cite_file_ids":["f2"],"query_type":"enumeration"}
{"id":"q3","question":"Why?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f3"],"query_type":"complex_reasoning"}
{"id":"q4","question":"Themes?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f4"],"query_type":"global_synthesis"}
`
	path := writeTempGolden(t, content)
	qs, err := LoadGoldenSet(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"lookup", "enumeration", "complex_reasoning", "global_synthesis"}
	for i, q := range qs {
		if q.QueryType != want[i] {
			t.Errorf("q[%d].QueryType = %q, want %q", i, q.QueryType, want[i])
		}
	}
}

func TestLoadGoldenSet_QueryTypeDefaultsToEmpty(t *testing.T) {
	// Pre-existing entries without query_type must still parse.
	content := `{"id":"q1","question":"What?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}`
	path := writeTempGolden(t, content)
	qs, err := LoadGoldenSet(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qs[0].QueryType != "" {
		t.Errorf("expected empty QueryType, got %q", qs[0].QueryType)
	}
}

func TestLoadGoldenSet_RejectsInvalidQueryType(t *testing.T) {
	content := `{"id":"q1","question":"What?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"query_type":"bogus"}`
	path := writeTempGolden(t, content)
	_, err := LoadGoldenSet(path)
	if err == nil {
		t.Fatal("expected error for invalid query_type")
	}
	if !strings.Contains(err.Error(), "query_type") {
		t.Errorf("expected error to mention 'query_type', got: %v", err)
	}
}

func TestLoadGoldenSet_ExampleFixture(t *testing.T) {
	// The repo's example fixture must always parse. Catches breakage
	// when the schema evolves.
	_, err := LoadGoldenSet("../../../eval/golden/example.jsonl")
	if err != nil {
		t.Fatalf("example.jsonl failed to parse: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseGoldenSetContent tests
// ---------------------------------------------------------------------------

func TestParseGoldenSetContent(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"00000000-0000-0000-0000-000000000000","language":"en","must_cite_file_ids":["00000000-0000-0000-0000-000000000001"],"query_type":"lookup"},
		{"id":"q2","question":"Q2?","kb_id":"00000000-0000-0000-0000-000000000000","language":"de","must_cite_file_ids":["00000000-0000-0000-0000-000000000002"]}
	]`)
	qs, err := ParseGoldenSetContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Errorf("got %d, want 2", len(qs))
	}
}

func TestParseGoldenSetContent_DuplicateID(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"00000000-0000-0000-0000-000000000000","language":"en","must_cite_file_ids":["00000000-0000-0000-0000-000000000001"]},
		{"id":"q1","question":"Q2?","kb_id":"00000000-0000-0000-0000-000000000000","language":"en","must_cite_file_ids":["00000000-0000-0000-0000-000000000002"]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("expected duplicate id error, got %v", err)
	}
}

// TestParseGoldenSetContent_RejectsTurns (finding F3): the DB/admin path
// must reject a multi-turn row outright rather than silently dropping the
// turns and running an empty top-level question, since ExpandTurns is
// never called on this path (runner_inproc.go, admin eval handlers).
func TestParseGoldenSetContent_RejectsTurns(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"conv1","kb_id":"kb-1","language":"en","turns":[
			{"question":"Who leads the project?","kind":"corpus","must_cite_file_names":["f1"]},
			{"question":"And who leads it?","kind":"pronoun_ref","must_cite_file_names":["f1"]}
		]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil {
		t.Fatal("expected error for a multi-turn row, got nil")
	}
	if !strings.Contains(err.Error(), "supported only by cmd/eval") {
		t.Errorf("expected cmd/eval-only rejection message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "conv1") {
		t.Errorf("expected the offending question id in the error, got: %v", err)
	}
}

// TestParseGoldenSetJSONL_StillAcceptsTurns locks in that the file-upload
// path (what cmd/eval itself reads) is unaffected by the DB-path rejection
// above — cmd/eval is the only caller of ExpandTurns and must keep working.
func TestParseGoldenSetJSONL_StillAcceptsTurns(t *testing.T) {
	content := `{"id":"conv1","kb_id":"kb-1","language":"en","turns":[{"question":"Who leads the project?","kind":"corpus","must_cite_file_names":["f1"]},{"question":"And who leads it?","kind":"pronoun_ref","must_cite_file_names":["f1"]}]}
`
	qs, err := ParseGoldenSetJSONL(strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	if len(qs[0].Turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(qs[0].Turns))
	}
}

func TestParseGoldenSetContent_InvalidJSON(t *testing.T) {
	_, err := ParseGoldenSetContent(json.RawMessage(`not-json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseGoldenSetContent_ValidationError(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "validate question 1") {
		t.Errorf("expected validate question 1 error, got %v", err)
	}
}

// AP-A5: ExpectedKBIDs is optional, but when present each entry must
// be non-empty. Empty strings inside the slice slip through type
// validation but corrupt the AP-A4 router scoring.
func TestParseGoldenSetContent_ExpectedKBIDsOptional(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]},
		{"id":"q2","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_kb_ids":["kb-1","kb-2"]}
	]`)
	qs, err := ParseGoldenSetContent(raw)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d, want 2", len(qs))
	}
	if len(qs[0].ExpectedKBIDs) != 0 {
		t.Errorf("q1 should default to empty ExpectedKBIDs, got %v", qs[0].ExpectedKBIDs)
	}
	if len(qs[1].ExpectedKBIDs) != 2 || qs[1].ExpectedKBIDs[0] != "kb-1" {
		t.Errorf("q2 ExpectedKBIDs not preserved: %v", qs[1].ExpectedKBIDs)
	}
}

func TestParseGoldenSetContent_ExpectedKBIDsRejectsEmpty(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_kb_ids":["kb-1","   "]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "expected_kb_ids[1] is empty") {
		t.Errorf("expected empty-entry error, got %v", err)
	}
}

// W4-R5: ExpectedPoints is optional; when present each point must be
// non-empty after trimming, at most 300 runes, and at most 12 total.
func TestParseGoldenSetContent_ExpectedPointsOptional(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]},
		{"id":"q2","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_points":["point A","point B"]}
	]`)
	qs, err := ParseGoldenSetContent(raw)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d, want 2", len(qs))
	}
	if len(qs[0].ExpectedPoints) != 0 {
		t.Errorf("q1 should default to empty ExpectedPoints, got %v", qs[0].ExpectedPoints)
	}
	if len(qs[1].ExpectedPoints) != 2 || qs[1].ExpectedPoints[0] != "point A" {
		t.Errorf("q2 ExpectedPoints not preserved: %v", qs[1].ExpectedPoints)
	}
}

func TestParseGoldenSetContent_ExpectedPointsRejectsEmptyPoint(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_points":["point A","   "]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "expected_points[1] is empty") {
		t.Errorf("expected empty-point error, got %v", err)
	}
}

func TestParseGoldenSetContent_ExpectedPointsRejectsOverLongPoint(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_points":["` + strings.Repeat("a", 301) + `"]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "expected_points[0] exceeds 300 runes") {
		t.Errorf("expected over-long-point error, got %v", err)
	}
}

func TestParseGoldenSetContent_ExpectedPointsRejectsTooMany(t *testing.T) {
	points := make([]string, 13)
	for i := range points {
		points[i] = `"p"`
	}
	raw := json.RawMessage(`[
		{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"],"expected_points":[` + strings.Join(points, ",") + `]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil || !strings.Contains(err.Error(), "at most 12 points allowed, got 13") {
		t.Errorf("expected too-many-points error, got %v", err)
	}
}

// TestSpreadsheetGoldenSetParses is the Phase 4 spreadsheet-ingest release
// acceptance golden set (spec .superpowers/sdd/2026-09-05-spreadsheet-ingest-phase4,
// Task 9; see eval/golden/README.md "Spreadsheet set" section and
// eval/golden/spreadsheets-de.acceptance.md for the run procedure and
// thresholds). It loads the committed file — kb_id is still the
// "REPLACE_WITH_FIXTURE_KB_ID" placeholder, a non-empty string, so
// LoadGoldenSet's shape validation passes without a live KB — and asserts:
//
//  1. at least 38 questions (~40 target, a couple may be pruned later);
//  2. every id is unique (LoadGoldenSet already enforces this, asserted
//     again here as a property of *this* file rather than of the loader);
//  3. every must_cite_file_names entry names a file that actually exists
//     under internal/sheetsource/testdata — this is what catches a stale
//     fixture reference (e.g. a rename) turning into a silent recall-0
//     regression instead of a loud test failure;
//  4. every query_type is one of the values the eval runner understands;
//  5. (Ruling R75) every question carries a non-nil tabular_expected —
//     the annotation is meant to be exhaustive, not partial, since
//     TabularRouterRates switches its whole eligibility rule the moment
//     ANY question in a report carries the field;
//  6. (Ruling R75) exactly 10 questions are tabular_expected=false — the
//     seven form-field/dropdown-list questions the router is right to
//     skip, plus the two "unanswerable" negatives, plus... (see the
//     literal id list in the task brief / project notes). A count check
//     rather than an id-by-id list so the test doesn't need updating
//     every time a question is added elsewhere in the set.
//
// Mutation check: renaming any must_cite_file_names entry so it no longer
// matches a file under internal/sheetsource/testdata (e.g. typo-ing
// "header_row14_metadata.xlsx" to "header_row14_metadata_typo.xlsx") turns
// this test red — see the Task 9 report for the transcript. For the R75
// assertions: setting any one question's tabular_expected to nil (drop the
// field) turns assertion 5 red; flipping one more question's flag to false
// turns assertion 6 red (11 != 10). See the Task 12 report for transcripts.
func TestSpreadsheetGoldenSetParses(t *testing.T) {
	const goldenPath = "../../../eval/golden/spreadsheets-de.jsonl"
	const testdataDir = "../sheetsource/testdata"

	qs, err := LoadGoldenSet(goldenPath)
	if err != nil {
		t.Fatalf("LoadGoldenSet(%s): %v", goldenPath, err)
	}
	if len(qs) < 38 {
		t.Fatalf("expected >= 38 questions, got %d", len(qs))
	}

	seenIDs := make(map[string]bool, len(qs))
	allowedQueryTypes := map[string]bool{
		"lookup":            true,
		"enumeration":       true,
		"global_synthesis":  true,
		"complex_reasoning": true,
	}
	falseCount := 0
	for _, question := range qs {
		if seenIDs[question.ID] {
			t.Errorf("duplicate id %q", question.ID)
		}
		seenIDs[question.ID] = true

		if question.KbID == "" {
			t.Errorf("question %s: empty kb_id", question.ID)
		}

		if len(question.MustCiteFileNames) == 0 {
			t.Errorf("question %s: must_cite_file_names is empty (this golden set is authored by name, not UUID)", question.ID)
		}
		for _, name := range question.MustCiteFileNames {
			path := filepath.Join(testdataDir, name)
			if _, statErr := os.Stat(path); statErr != nil {
				t.Errorf("question %s: must_cite_file_names %q does not exist under %s (%v)", question.ID, name, testdataDir, statErr)
			}
		}

		if !allowedQueryTypes[question.QueryType] {
			t.Errorf("question %s: query_type %q is not one of lookup|enumeration|global_synthesis|complex_reasoning", question.ID, question.QueryType)
		}

		if question.TabularExpected == nil {
			t.Errorf("question %s: tabular_expected is absent, want a non-nil bool (R75 annotation must be exhaustive)", question.ID)
			continue
		}
		if !*question.TabularExpected {
			falseCount++
		}
	}
	if falseCount != 10 {
		t.Errorf("tabular_expected=false count = %d, want 10", falseCount)
	}
}

// ---------------------------------------------------------------------------
// ParseGoldenSetContent: JSONL support (W6-R3)
// ---------------------------------------------------------------------------

// TestParseGoldenSetContent_JSONLWithComments: the in-app/DB path must accept
// JSONL (the shape a file upload carries) with '#' comment and blank lines,
// sharing ParseGoldenSetJSONL's line parser — and the same rows as a JSON
// array must deep-equal the JSONL result.
func TestParseGoldenSetContent_JSONLWithComments(t *testing.T) {
	row1 := `{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}`
	row2 := `{"id":"q2","question":"Q2?","kb_id":"kb-1","language":"de","must_cite_file_ids":["f2"]}`
	jsonl := "# header\n\n" + row1 + "\n" + row2

	qs, err := ParseGoldenSetContent(json.RawMessage(jsonl))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d questions, want 2", len(qs))
	}
	if qs[0].ID != "q1" || qs[1].ID != "q2" {
		t.Errorf("unexpected ids in order: %q, %q", qs[0].ID, qs[1].ID)
	}

	arr := "[" + row1 + "," + row2 + "]"
	arrQs, err := ParseGoldenSetContent(json.RawMessage(arr))
	if err != nil {
		t.Fatalf("unexpected error (array form): %v", err)
	}
	if !reflect.DeepEqual(qs, arrQs) {
		t.Errorf("JSONL and array forms of the same rows produced different results:\nJSONL: %+v\narray: %+v", qs, arrQs)
	}
}

// TestParseGoldenSetContent_ArrayStillParses: the pre-existing array path,
// including leading whitespace/newlines before the '[', keeps working.
func TestParseGoldenSetContent_ArrayStillParses(t *testing.T) {
	raw := json.RawMessage("\n  [\n" +
		`{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]},` + "\n" +
		`{"id":"q2","question":"Q2?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f2"]}` + "\n]\n")
	qs, err := ParseGoldenSetContent(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d questions, want 2", len(qs))
	}
}

// TestParseGoldenSetContent_BareObjectRowIsNotAComment: a single JSONL row
// with no leading '#' (and no surrounding '[') must still parse as JSONL.
func TestParseGoldenSetContent_BareObjectRowIsNotAComment(t *testing.T) {
	raw := json.RawMessage(`{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}`)
	qs, err := ParseGoldenSetContent(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("got %d questions, want 1", len(qs))
	}
}

// TestParseGoldenSetContent_GarbageErrors: neither shape detector should
// mistake malformed input for a valid empty result.
func TestParseGoldenSetContent_GarbageErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "only a comment", content: "# only a comment", wantErr: "no questions"},
		{name: "truncated array", content: "[1,2", wantErr: ""},
		{name: "truncated object", content: `{"id":`, wantErr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGoldenSetContent(json.RawMessage(tc.content))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestParseGoldenSetContent_RejectsTurnsInJSONL: the "multi-turn rows are
// supported only by cmd/eval" rejection (see TestParseGoldenSetContent_RejectsTurns
// for the array form) must also fire on a turns row submitted as JSONL.
func TestParseGoldenSetContent_RejectsTurnsInJSONL(t *testing.T) {
	content := `{"id":"conv1","kb_id":"kb-1","language":"en","turns":[{"question":"Who leads the project?","kind":"corpus","must_cite_file_names":["f1"]},{"question":"And who leads it?","kind":"pronoun_ref","must_cite_file_names":["f1"]}]}`
	_, err := ParseGoldenSetContent(json.RawMessage(content))
	if err == nil {
		t.Fatal("expected error for a multi-turn JSONL row, got nil")
	}
	if !strings.Contains(err.Error(), "supported only by cmd/eval") {
		t.Errorf("expected cmd/eval-only rejection message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "conv1") {
		t.Errorf("expected the offending question id in the error, got: %v", err)
	}
}

// TestParseGoldenSetContent_ArrayPrecedence_ValidationBeforeLaterTurns pins
// the array path's per-index priority: an earlier row's validation error
// must win over a later row's turns error, exactly as the original single
// loop (turns check, then validate, then duplicate-id, per row, in document
// order) behaved before JSONL support existed. This is the reviewer's exact
// two-row reproduction from task-3-review.md, guarding against a whole-array
// turns pre-pass silently changing error precedence.
func TestParseGoldenSetContent_ArrayPrecedence_ValidationBeforeLaterTurns(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"q1","question":"","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]},
		{"id":"conv2","kb_id":"kb-1","language":"en","turns":[
			{"question":"Who leads the project?","kind":"corpus","must_cite_file_names":["f1"]}
		]}
	]`)
	_, err := ParseGoldenSetContent(raw)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	const want = "validate question 1: missing question"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// TestParseGoldenSetContent_JSONLDuplicateID: the "duplicate ids rejected on
// both shapes" claim in the doc comment needs a direct JSONL-path test, not
// only the array-path TestParseGoldenSetContent_DuplicateID.
func TestParseGoldenSetContent_JSONLDuplicateID(t *testing.T) {
	content := `{"id":"q1","question":"Q?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f1"]}
{"id":"q1","question":"Q2?","kb_id":"kb-1","language":"en","must_cite_file_ids":["f2"]}`
	_, err := ParseGoldenSetContent(json.RawMessage(content))
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("expected duplicate id error, got %v", err)
	}
}

// TestParseGoldenSetContent_RealJSONLFixture is a one-off manual check
// (controller ruling, Wave 6 Task 3): run the real 24-question
// global-synthesis-de.jsonl set — which starts with '#' comment lines —
// through ParseGoldenSetContent and confirm the count. Skipped unless
// EVAL_JSONL_PATH is set, so it is harmless in normal CI/test runs:
//
//	EVAL_JSONL_PATH=/path/to/eval/golden/global-synthesis-de.jsonl \
//	  go test ./internal/eval -run TestParseGoldenSetContent_RealJSONLFixture -v
func TestParseGoldenSetContent_RealJSONLFixture(t *testing.T) {
	path := os.Getenv("EVAL_JSONL_PATH")
	if path == "" {
		t.Skip("EVAL_JSONL_PATH not set; skipping manual fixture check")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	qs, err := ParseGoldenSetContent(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseGoldenSetContent(%s): %v", path, err)
	}
	t.Logf("parsed %d questions from %s", len(qs), path)
	if len(qs) != 24 {
		t.Errorf("got %d questions, want 24", len(qs))
	}
}
