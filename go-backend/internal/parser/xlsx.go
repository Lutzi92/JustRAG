package parser

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// XLSXParser converts Excel spreadsheets into a markdown table representation
// with structure-aware chunking: each chunk repeats the header row so that
// every chunk is independently understandable.  Multiple sheets are each
// rendered with a heading and joined with blank lines.
type XLSXParser struct{}

func (p *XLSXParser) Name() string { return "XLSXParser" }

// CanParse returns true for the modern Excel MIME type or the .xlsx extension.
func (p *XLSXParser) CanParse(mimeType, fileName string) bool {
	if mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		return true
	}
	return strings.HasSuffix(fileName, ".xlsx")
}

// Parse reads the XLSX file and converts every sheet to a markdown table.
// Large sheets are split into chunks that each repeat the header block so they
// remain self-contained.  Up to ExcelMaxRows data rows are read per sheet.
func (p *XLSXParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	f, err := excelize.OpenFile(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("xlsx: opening file: %w", err)
	}
	defer f.Close()

	// Determine chunk size in tokens; fall back to 512 if unset.
	chunkSize := pctx.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512
	}
	// Target ~80 % of chunkSize tokens per chunk.
	tokenBudget := int(float64(chunkSize) * 0.8)

	sheets := f.GetSheetList()
	var sheetParts []string

	for _, sheetName := range sheets {
		rows, err := f.GetRows(sheetName)
		if err != nil {
			return nil, fmt.Errorf("xlsx: reading sheet %q: %w", sheetName, err)
		}

		part := renderSheet(sheetName, rows, tokenBudget)
		sheetParts = append(sheetParts, part)
	}

	return &ParseResult{Text: strings.Join(sheetParts, "\n\n")}, nil
}

// renderSheet converts a single sheet's rows into one or more markdown table
// chunks, preceded by a sheet heading.
func renderSheet(sheetName string, rows [][]string, tokenBudget int) string {
	heading := fmt.Sprintf("### Sheet: %s", sheetName)

	if len(rows) == 0 {
		return heading
	}

	headers := rows[0]
	dataRows := rows[1:]

	// Cap to ExcelMaxRows data rows.
	if len(dataRows) > ExcelMaxRows {
		dataRows = dataRows[:ExcelMaxRows]
	}

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

	for _, row := range dataRows {
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
		// No data rows — return heading + just the header table.
		return heading + "\n\n" + strings.TrimRight(headerBlock, "\n")
	}

	return heading + "\n\n" + strings.Join(chunks, "\n\n")
}
