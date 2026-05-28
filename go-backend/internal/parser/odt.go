package parser

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const (
	odtTextNamespace = "urn:oasis:names:tc:opendocument:xmlns:text:1.0"
	odtMimeType      = "application/vnd.oasis.opendocument.text"
	odtContentXML    = "content.xml"
)

// OdtParser extracts plain text from ODT (OpenDocument Text) files by reading
// the content.xml entry inside the ZIP archive and walking its XML tokens.
type OdtParser struct{}

func (p *OdtParser) Name() string { return "OdtParser" }

// CanParse returns true for the standard ODT MIME type or the .odt extension.
func (p *OdtParser) CanParse(mimeType, fileName string) bool {
	return mimeType == odtMimeType || strings.HasSuffix(strings.ToLower(fileName), ".odt")
}

// Parse opens the ODT archive, locates content.xml, and extracts text content
// from <text:p>, <text:h>, and <text:span> elements.  Paragraphs and headings
// are separated by newlines.
func (p *OdtParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	zr, err := zip.OpenReader(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("odt: open zip: %w", err)
	}
	defer zr.Close()

	// Locate content.xml inside the archive.
	var contentFile *zip.File
	for _, f := range zr.File {
		if f.Name == odtContentXML {
			contentFile = f
			break
		}
	}
	if contentFile == nil {
		return nil, fmt.Errorf("odt: %s not found in archive", odtContentXML)
	}

	rc, err := contentFile.Open()
	if err != nil {
		return nil, fmt.Errorf("odt: open %s: %w", odtContentXML, err)
	}
	defer rc.Close()

	text, err := extractOdtText(rc)
	if err != nil {
		return nil, fmt.Errorf("odt: extract text: %w", err)
	}

	return &ParseResult{Text: text}, nil
}

// extractOdtText walks the XML token stream of content.xml and reconstructs
// the document's text content.
//
// Rules:
//   - <text:p> or <text:h> start → begin a new paragraph buffer
//   - <text:p> or <text:h> end   → flush the paragraph buffer
//   - CharData inside a text element → appended to current paragraph
//   - <text:s>          → space character
//   - <text:tab>        → tab character
//   - <text:line-break> → newline within a paragraph
//
// Paragraphs are joined with a single newline.
func extractOdtText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)

	var paragraphs []string
	var currentPara strings.Builder
	depth := 0 // nesting depth inside a paragraph/heading element

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space == odtTextNamespace {
				switch t.Name.Local {
				case "p", "h":
					depth++
					if depth == 1 {
						currentPara.Reset()
					}
				case "span":
					if depth > 0 {
						depth++
					}
				case "s":
					// <text:s> represents one or more spaces; treat as single space.
					if depth > 0 {
						currentPara.WriteByte(' ')
					}
				case "tab":
					if depth > 0 {
						currentPara.WriteByte('\t')
					}
				case "line-break":
					if depth > 0 {
						currentPara.WriteByte('\n')
					}
				}
			}

		case xml.EndElement:
			if t.Name.Space == odtTextNamespace {
				switch t.Name.Local {
				case "p", "h":
					if depth > 0 {
						depth--
						if depth == 0 {
							paragraphs = append(paragraphs, currentPara.String())
							currentPara.Reset()
						}
					}
				case "span":
					if depth > 0 {
						depth--
					}
				}
			}

		case xml.CharData:
			if depth > 0 {
				currentPara.Write(t)
			}

		case xml.ProcInst, xml.Comment, xml.Directive:
			// Ignore.
		}
	}

	result := strings.Join(paragraphs, "\n")
	result = strings.TrimRight(result, "\n")
	return result, nil
}
