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
	wordNamespace   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	docxMimeType    = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	wordDocumentXML = "word/document.xml"
)

// DocxParser extracts plain text from DOCX (Office Open XML) files by reading
// the word/document.xml entry inside the ZIP archive and walking its XML
// tokens.
type DocxParser struct{}

func (p *DocxParser) Name() string { return "DocxParser" }

// CanParse returns true for the standard DOCX MIME type or the .docx extension.
func (p *DocxParser) CanParse(mimeType, fileName string) bool {
	return mimeType == docxMimeType || strings.HasSuffix(strings.ToLower(fileName), ".docx")
}

// Parse opens the DOCX archive, locates word/document.xml, and extracts text
// content from <w:t> elements.  Paragraphs (<w:p>) are separated by newlines.
// Line breaks (<w:br/>) within a paragraph become newlines as well.
func (p *DocxParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	zr, err := zip.OpenReader(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("docx: open zip: %w", err)
	}
	defer zr.Close()

	// Locate word/document.xml inside the archive.
	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == wordDocumentXML {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return nil, fmt.Errorf("docx: %s not found in archive", wordDocumentXML)
	}

	rc, err := docFile.Open()
	if err != nil {
		return nil, fmt.Errorf("docx: open %s: %w", wordDocumentXML, err)
	}
	defer rc.Close()

	text, err := extractDocxText(rc)
	if err != nil {
		return nil, fmt.Errorf("docx: extract text: %w", err)
	}

	return &ParseResult{Text: text}, nil
}

// extractDocxText walks the XML token stream of word/document.xml and
// reconstructs the document's text content.
//
// Rules:
//   - <w:p> start → begin a new paragraph buffer
//   - <w:t> start → read the next CharData token as text content
//   - <w:br/> → append a newline to the current paragraph
//   - <w:p> end → flush the paragraph buffer (trim trailing newlines)
//
// Paragraphs are joined with a single newline.  Leading/trailing blank
// paragraphs in the result are trimmed.
func extractDocxText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)

	var paragraphs []string
	var currentPara strings.Builder
	inParagraph := false

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
			if t.Name.Space == wordNamespace {
				switch t.Name.Local {
				case "p":
					inParagraph = true
					currentPara.Reset()
				case "t":
					if inParagraph {
						var text string
						if err := decoder.DecodeElement(&text, &t); err != nil {
							return "", err
						}
						currentPara.WriteString(text)
					}
				case "br":
					// <w:br/> is a line break within a paragraph.  In Go's
					// encoding/xml a self-closing element appears as a
					// StartElement immediately followed by an EndElement; we
					// emit the newline here and the EndElement is consumed by
					// the loop on the next iteration.
					if inParagraph {
						currentPara.WriteByte('\n')
					}
				}
			}

		case xml.EndElement:
			if t.Name.Space == wordNamespace && t.Name.Local == "p" && inParagraph {
				paragraphs = append(paragraphs, currentPara.String())
				currentPara.Reset()
				inParagraph = false
			}

		case xml.ProcInst, xml.Comment, xml.Directive:
			// Ignore processing instructions, comments, and directives.
		}
	}

	// Filter out empty paragraphs at the edges but preserve internal ones
	// represented as empty strings (they contribute blank lines).
	result := strings.Join(paragraphs, "\n")
	result = strings.TrimRight(result, "\n")
	return result, nil
}
