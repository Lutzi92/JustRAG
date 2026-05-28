package tabular

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

// maxRows caps rows read per sheet. 2,000,000 covers the 1M-row target with
// headroom; a defensive bound against a pathological file exhausting memory.
const maxRows = 2_000_000

// IsSpreadsheet reports whether a file should route through the tabular
// materializer, by MIME type or extension. Mirrors the parser CanParse checks.
func IsSpreadsheet(mimeType, fileName string) bool {
	switch mimeType {
	case "text/csv",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.ms-excel":
		return true
	}
	lower := strings.ToLower(fileName)
	return strings.HasSuffix(lower, ".csv") ||
		strings.HasSuffix(lower, ".xlsx") ||
		strings.HasSuffix(lower, ".xls")
}

// ReadSheets reads every sheet's raw rows (header row included).
//
// NOTE (Phase 1): each sheet is fully buffered into memory ([][]string) before
// COPY, capped at maxRows. At the 1M-row target this is a multi-hundred-MB
// resident spike per ingest. Streaming rows straight into pgx.CopyFrom (with a
// buffered prefix for type inference) is a known follow-up; see the design spec.
func ReadSheets(filePath, fileName string) ([]SheetData, error) {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".csv"):
		return readCSV(filePath)
	case strings.HasSuffix(lower, ".xlsx"):
		return readXLSX(filePath)
	case strings.HasSuffix(lower, ".xls"):
		// excelize reads the OOXML .xlsx format; genuine legacy BIFF .xls files
		// will error here and the processor falls back to text ingestion. (A
		// dedicated legacy-.xls reader is out of scope for Phase 1.)
		return readXLSX(filePath)
	default:
		return readCSV(filePath)
	}
}

func readCSV(filePath string) ([]SheetData, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	var rows [][]string
	for len(rows) < maxRows {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tabular: csv read: %w", err)
		}
		rows = append(rows, rec)
	}
	return []SheetData{{SheetName: "Sheet1", Rows: rows}}, nil
}

func readXLSX(filePath string) ([]SheetData, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("tabular: open xlsx: %w", err)
	}
	defer f.Close()
	var out []SheetData
	for _, name := range f.GetSheetList() {
		it, err := f.Rows(name)
		if err != nil {
			return nil, fmt.Errorf("tabular: rows(%q): %w", name, err)
		}
		var rows [][]string
		for it.Next() && len(rows) < maxRows {
			cols, err := it.Columns()
			if err != nil {
				it.Close()
				return nil, fmt.Errorf("tabular: columns(%q): %w", name, err)
			}
			rows = append(rows, cols)
		}
		it.Close()
		if len(rows) == 0 {
			continue
		}
		out = append(out, SheetData{SheetName: name, Rows: rows})
	}
	return out, nil
}
