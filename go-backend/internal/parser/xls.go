package parser

import (
	"context"
	"fmt"
	"strings"

	"github.com/extrame/xls"
)

// XLSParser converts legacy Excel (.xls) spreadsheets into a markdown table
// representation using the same chunked format as XLSXParser.
type XLSParser struct{}

func (p *XLSParser) Name() string { return "xls" }

// CanParse returns true for the legacy Excel MIME type or the .xls extension.
func (p *XLSParser) CanParse(mimeType, fileName string) bool {
	if mimeType == "application/vnd.ms-excel" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(fileName), ".xls")
}

// Parse reads the XLS file and converts every sheet to a markdown table.
func (p *XLSParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	wb, err := xls.Open(pctx.FilePath, "utf-8")
	if err != nil {
		return nil, fmt.Errorf("xls: opening file: %w", err)
	}

	chunkSize := pctx.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512
	}
	tokenBudget := int(float64(chunkSize) * 0.8)

	var sheetParts []string
	totalChars := 0

	for i := 0; i < wb.NumSheets(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		sheet := wb.GetSheet(i)
		if sheet == nil {
			continue
		}

		sheetName := sheet.Name
		heading := fmt.Sprintf("### Sheet: %s", sheetName)

		maxRow := int(sheet.MaxRow)
		if maxRow < 0 {
			sheetParts = append(sheetParts, heading)
			continue
		}

		// Read all rows into a [][]string.
		var rows [][]string
		for r := 0; r <= maxRow; r++ {
			if r%100 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			row := sheet.Row(r)
			if row == nil {
				continue
			}
			var cells []string
			lastCol := row.LastCol()
			for c := 0; c < lastCol; c++ {
				cells = append(cells, row.Col(c))
			}
			rows = append(rows, cells)
			// Cap data rows (excluding header).
			if len(rows) >= ExcelMaxRows+1 {
				break
			}
		}

		if len(rows) == 0 {
			sheetParts = append(sheetParts, heading)
			continue
		}

		part := renderSheet(sheetName, rows, tokenBudget)
		totalChars += len(part)
		if totalChars > TextMaxChars {
			sheetParts = append(sheetParts, part[:len(part)-(totalChars-TextMaxChars)])
			break
		}
		sheetParts = append(sheetParts, part)
	}

	return &ParseResult{Text: strings.Join(sheetParts, "\n\n")}, nil
}
