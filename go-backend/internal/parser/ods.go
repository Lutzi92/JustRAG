package parser

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// OdsParser converts OpenDocument Spreadsheet (.ods) files into a markdown
// table representation using the same chunked format as XLSXParser.
type OdsParser struct{}

func (p *OdsParser) Name() string { return "ods" }

// CanParse returns true for the ODS MIME type or the .ods extension.
func (p *OdsParser) CanParse(mimeType, fileName string) bool {
	if mimeType == "application/vnd.oasis.opendocument.spreadsheet" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(fileName), ".ods")
}

// Parse reads the ODS file (a ZIP containing content.xml) and converts every
// sheet to a markdown table.
func (p *OdsParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	sheets, err := readODSSheets(ctx, pctx.FilePath)
	if err != nil {
		return nil, err
	}

	chunkSize := pctx.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512
	}
	tokenBudget := int(float64(chunkSize) * 0.8)

	var sheetParts []string
	totalChars := 0

	for _, s := range sheets {
		part := renderSheet(s.name, s.rows, tokenBudget)
		totalChars += len(part)
		if totalChars > TextMaxChars {
			overflow := totalChars - TextMaxChars
			if overflow >= len(part) {
				break // entire part exceeds budget
			}
			sheetParts = append(sheetParts, part[:len(part)-overflow])
			break
		}
		sheetParts = append(sheetParts, part)
	}

	return &ParseResult{Text: strings.Join(sheetParts, "\n\n")}, nil
}

type odsSheet struct {
	name string
	rows [][]string
}

// readODSSheets opens the ODS ZIP, locates content.xml, and extracts table
// data using an XML stream tokenizer.
func readODSSheets(ctx context.Context, filePath string) ([]odsSheet, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("ods: opening zip: %w", err)
	}
	defer zr.Close()

	var contentFile *zip.File
	for _, f := range zr.File {
		if f.Name == "content.xml" {
			contentFile = f
			break
		}
	}
	if contentFile == nil {
		return nil, fmt.Errorf("ods: content.xml not found in archive")
	}

	rc, err := contentFile.Open()
	if err != nil {
		return nil, fmt.Errorf("ods: opening content.xml: %w", err)
	}
	defer rc.Close()

	return parseODSContentXML(ctx, rc)
}

// parseODSContentXML parses the ODS content.xml stream and extracts sheets
// with their rows and cells.
func parseODSContentXML(ctx context.Context, r io.Reader) ([]odsSheet, error) {
	decoder := xml.NewDecoder(r)
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose

	var sheets []odsSheet
	var currentSheet *odsSheet
	var currentRow []string
	inRow := false
	inCell := false
	cellRepeat := 1
	var cellText strings.Builder
	totalRows := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ods: parsing content.xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local
			switch local {
			case "table":
				name := ""
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						name = a.Value
						break
					}
				}
				sheets = append(sheets, odsSheet{name: name})
				currentSheet = &sheets[len(sheets)-1]

			case "table-row":
				inRow = true
				currentRow = nil

			case "table-cell":
				inCell = true
				cellText.Reset()
				cellRepeat = 1
				for _, a := range t.Attr {
					if a.Name.Local == "number-columns-repeated" {
						n := 0
						fmt.Sscanf(a.Value, "%d", &n)
						if n > 1 {
							cellRepeat = n
						}
						// Cap to prevent excessive expansion from
						// malformed/malicious files.
						const maxCellRepeat = 16384
						if cellRepeat > maxCellRepeat {
							cellRepeat = maxCellRepeat
						}
					}
				}

			case "p":
				// Text paragraphs inside cells — add space separator
				// for multi-paragraph cells.
				if inCell && cellText.Len() > 0 {
					cellText.WriteByte(' ')
				}
			}

		case xml.EndElement:
			local := t.Name.Local
			switch local {
			case "table":
				currentSheet = nil

			case "table-row":
				if inRow && currentSheet != nil {
					// Skip completely empty trailing rows.
					if hasNonEmpty(currentRow) {
						currentSheet.rows = append(currentSheet.rows, currentRow)
						totalRows++
						if totalRows >= ExcelMaxRows+1 { // +1 for header
							return sheets, nil
						}
					}
				}
				inRow = false
				currentRow = nil

			case "table-cell":
				if inCell && inRow {
					text := cellText.String()
					for i := 0; i < cellRepeat; i++ {
						currentRow = append(currentRow, text)
					}
				}
				inCell = false

			case "p":
				// handled in CharData
			}

		case xml.CharData:
			if inCell {
				cellText.Write(t)
			}
		}
	}

	return sheets, nil
}

// hasNonEmpty returns true if at least one cell in the row is non-empty.
func hasNonEmpty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return true
		}
	}
	return false
}
