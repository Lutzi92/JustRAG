package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseGoldenSetContent decodes the JSONB content of an eval_golden_sets row
// into a slice of Questions. Applies the same validation as LoadGoldenSet.
// Returns an error with a 1-based index into the array when any question fails
// validation.
func ParseGoldenSetContent(raw json.RawMessage) ([]Question, error) {
	var qs []Question
	if err := json.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("parse golden set content: %w", err)
	}
	seen := make(map[string]int, len(qs))
	for i, q := range qs {
		if err := validateQuestion(q); err != nil {
			return nil, fmt.Errorf("validate question %d: %w", i+1, err)
		}
		if prev, ok := seen[q.ID]; ok {
			return nil, fmt.Errorf("duplicate id %q at index %d (first seen at %d)", q.ID, i+1, prev+1)
		}
		seen[q.ID] = i
	}
	return qs, nil
}

// ParseGoldenSetJSONL parses JSONL content from r and returns validated Question entries.
// Blank lines and lines starting with '#' are ignored (comments).
// Required: id, question, kb_id, language ("de"|"en"), non-empty must_cite_file_ids.
// This is the io.Reader variant of LoadGoldenSet — useful for in-memory content
// such as multipart file uploads.
func ParseGoldenSetJSONL(r io.Reader) ([]Question, error) {
	var questions []Question
	seenIDs := make(map[string]int)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var q Question
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("parse line %d: %w", lineNum, err)
		}
		if err := validateQuestion(q); err != nil {
			return nil, fmt.Errorf("validate line %d: %w", lineNum, err)
		}
		if firstLine, ok := seenIDs[q.ID]; ok {
			return nil, fmt.Errorf("duplicate id %q at line %d (first seen at line %d)", q.ID, lineNum, firstLine)
		}
		seenIDs[q.ID] = lineNum
		questions = append(questions, q)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan golden set: %w", err)
	}
	return questions, nil
}

// LoadGoldenSet reads a JSONL file from path and returns parsed Question entries.
// Blank lines and lines starting with '#' are ignored (comments).
// Required: id, question, kb_id, language ("de"|"en"), non-empty must_cite_file_ids.
func LoadGoldenSet(path string) ([]Question, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open golden set: %w", err)
	}
	defer f.Close()
	return ParseGoldenSetJSONL(f)
}

func validateQuestion(q Question) error {
	if q.ID == "" {
		return fmt.Errorf("missing id")
	}
	if q.Question == "" {
		return fmt.Errorf("missing question")
	}
	if q.KbID == "" {
		return fmt.Errorf("missing kb_id")
	}
	if q.Language != "de" && q.Language != "en" {
		return fmt.Errorf("language must be 'de' or 'en', got %q", q.Language)
	}
	if len(q.MustCiteFileIDs) == 0 && len(q.MustCiteFileNames) == 0 {
		return fmt.Errorf("must_cite_file_ids or must_cite_file_names must be non-empty")
	}
	for i, id := range q.MustCiteFileIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("must_cite_file_ids[%d] is empty", i)
		}
	}
	for i, name := range q.MustCiteFileNames {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("must_cite_file_names[%d] is empty", i)
		}
	}
	switch q.QueryType {
	case "", "lookup", "enumeration", "global_synthesis", "complex_reasoning":
		// accepted
	default:
		return fmt.Errorf("query_type must be one of lookup|enumeration|global_synthesis|complex_reasoning, got %q", q.QueryType)
	}
	// AP-A4 ExpectedKBIDs: optional. When present every entry must be
	// non-empty; KbID need not appear in the list (a multi-KB question
	// might list both expected KBs and not duplicate the "primary" one).
	for i, id := range q.ExpectedKBIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("expected_kb_ids[%d] is empty", i)
		}
	}
	return nil
}
