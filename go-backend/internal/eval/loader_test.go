package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// TestLoadGoldenSet_MultiHop: the AP-C5 multi_hop.jsonl skeleton must
// parse cleanly AND surface at least one row with two or more
// must_cite_file_ids — the fixture's whole purpose is multi-source
// retrieval. A regression that empties or single-files the file
// would silently make Phase C's eval-gate trivially pass.
func TestLoadGoldenSet_MultiHop(t *testing.T) {
	t.Parallel()
	qs, err := LoadGoldenSet("../../../eval/golden/multi_hop.jsonl")
	if err != nil {
		t.Fatalf("multi_hop.jsonl failed to parse: %v", err)
	}
	if len(qs) == 0 {
		t.Fatal("multi_hop.jsonl parsed to zero questions")
	}
	hasMulti := false
	for _, q := range qs {
		if len(q.MustCiteFileIDs) >= 2 {
			hasMulti = true
			break
		}
	}
	if !hasMulti {
		t.Error("multi_hop.jsonl: every row had <2 must_cite_file_ids; the multi-hop premise is gone")
	}
}

// TestLoadGoldenSet_MultiKB: the AP-A5 multi_kb.jsonl skeleton committed
// alongside this CL must parse cleanly. Catches regressions when the
// schema is extended without updating the fixture.
func TestLoadGoldenSet_MultiKB(t *testing.T) {
	t.Parallel()
	qs, err := LoadGoldenSet("../../../eval/golden/multi_kb.jsonl")
	if err != nil {
		t.Fatalf("multi_kb.jsonl failed to parse: %v", err)
	}
	if len(qs) == 0 {
		t.Fatal("multi_kb.jsonl parsed to zero questions")
	}
	hasMulti := false
	for _, q := range qs {
		if len(q.ExpectedKBIDs) > 1 {
			hasMulti = true
			break
		}
	}
	if !hasMulti {
		t.Error("multi_kb.jsonl: no row with len(ExpectedKBIDs) > 1; the cross-KB section is empty")
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
