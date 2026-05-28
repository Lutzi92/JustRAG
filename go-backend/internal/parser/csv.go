package parser

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

// CSVParser converts CSV files into a markdown table representation with
// structure-aware chunking: each chunk repeats the header row so that every
// chunk is independently understandable.
type CSVParser struct{}

func (p *CSVParser) Name() string { return "CSVParser" }

// CanParse returns true for the text/csv MIME type or the .csv extension.
func (p *CSVParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "text/csv" || strings.HasSuffix(fileName, ".csv")
}

// Parse reads the CSV file and converts it to a markdown table.  Large tables
// are split into chunks that each repeat the header block so they remain
// self-contained.  Up to ExcelMaxRows data rows are read.
func (p *CSVParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	f, err := os.Open(pctx.FilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	// Read header row.
	headers, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return &ParseResult{Text: ""}, nil
		}
		return nil, fmt.Errorf("csv: reading header: %w", err)
	}

	// Read data rows up to ExcelMaxRows.
	var rows [][]string
	for len(rows) < ExcelMaxRows {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv: reading row %d: %w", len(rows)+1, err)
		}
		rows = append(rows, row)
	}

	// Determine chunk size in tokens; fall back to 512 if unset.
	chunkSize := pctx.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512
	}
	// Target ~80 % of chunkSize tokens per chunk.
	tokenBudget := int(float64(chunkSize) * 0.8)

	// Build the reusable markdown header block.
	headerBlock := buildMarkdownHeader(headers)

	// Split data rows into chunks that fit within the token budget.
	var chunks []string
	var chunkRows [][]string
	chunkTokens := EstimateTokens(headerBlock)

	flush := func() {
		if len(chunkRows) == 0 {
			return
		}
		var sb strings.Builder
		sb.WriteString(headerBlock)
		for _, row := range chunkRows {
			sb.WriteString(markdownRow(row, len(headers)))
			sb.WriteByte('\n')
		}
		chunks = append(chunks, strings.TrimRight(sb.String(), "\n"))
		chunkRows = nil
		chunkTokens = EstimateTokens(headerBlock)
	}

	for _, row := range rows {
		rowText := markdownRow(row, len(headers))
		rowTokens := EstimateTokens(rowText)
		if len(chunkRows) > 0 && chunkTokens+rowTokens > tokenBudget {
			flush()
		}
		chunkRows = append(chunkRows, row)
		chunkTokens += rowTokens
	}
	flush()

	if len(chunks) == 0 {
		// No data rows — return just the header.
		return &ParseResult{Text: strings.TrimRight(headerBlock, "\n")}, nil
	}

	return &ParseResult{Text: strings.Join(chunks, "\n\n")}, nil
}

// buildMarkdownHeader returns the two-line markdown table header:
//
//	| col1 | col2 |
//	| --- | --- |
func buildMarkdownHeader(headers []string) string {
	var sb strings.Builder
	sb.WriteByte('|')
	for _, h := range headers {
		sb.WriteString(" ")
		sb.WriteString(strings.ReplaceAll(h, "|", "\\|"))
		sb.WriteString(" |")
	}
	sb.WriteByte('\n')
	sb.WriteByte('|')
	for range headers {
		sb.WriteString(" --- |")
	}
	sb.WriteByte('\n')
	return sb.String()
}

// markdownRow formats a single CSV row as a markdown table row.  If the row
// has fewer columns than expected, the missing cells are left empty.
func markdownRow(row []string, numCols int) string {
	var sb strings.Builder
	sb.WriteByte('|')
	for i := 0; i < numCols; i++ {
		cell := ""
		if i < len(row) {
			cell = strings.ReplaceAll(row[i], "|", "\\|")
		}
		sb.WriteString(" ")
		sb.WriteString(cell)
		sb.WriteString(" |")
	}
	return sb.String()
}
