package parser

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// slideRe matches slide XML paths and extracts the slide number.
var slideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

// PptxParser extracts text from PowerPoint (.pptx) files by reading slide XMLs
// from the underlying ZIP archive and parsing DrawingML <a:t> elements.
type PptxParser struct{}

func (p *PptxParser) Name() string { return "PptxParser" }

// CanParse returns true for the OOXML presentation MIME type or the .pptx extension.
func (p *PptxParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/vnd.openxmlformats-officedocument.presentationml.presentation" ||
		strings.HasSuffix(strings.ToLower(fileName), ".pptx")
}

// readZipFile reads a named file from a pre-built ZIP file index, returning its
// contents up to a 1 MB limit. Returns nil if the file is not found.
func readZipFile(fileIndex map[string]*zip.File, name string) []byte {
	const maxSize = 1 << 20 // 1 MB
	f, ok := fileIndex[name]
	if !ok {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxSize))
	if err != nil {
		return nil
	}
	return data
}

// parseSlideOrderFromXML extracts the canonical slide ordering from
// ppt/presentation.xml and ppt/_rels/presentation.xml.rels. It returns an
// ordered list of slide file paths (e.g. "ppt/slides/slide2.xml"), or nil if
// parsing fails or the required data is missing.
func parseSlideOrderFromXML(presXML, relsXML []byte) []string {
	if len(presXML) == 0 || len(relsXML) == 0 {
		return nil
	}

	// Step 1: Parse rels XML to build rId → target map.
	rIdToTarget := make(map[string]string)
	dec := xml.NewDecoder(bytes.NewReader(relsXML))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "Relationship" {
			var id, target string
			for _, attr := range se.Attr {
				switch attr.Name.Local {
				case "Id":
					id = attr.Value
				case "Target":
					target = attr.Value
				}
			}
			if id != "" && target != "" {
				// Normalize target: strip leading slash for absolute
				// paths, and only prepend "ppt/" for relative targets.
				target = strings.TrimPrefix(target, "/")
				if !strings.HasPrefix(target, "ppt/") {
					target = "ppt/" + target
				}
				rIdToTarget[id] = target
			}
		}
	}

	if len(rIdToTarget) == 0 {
		return nil
	}

	// Step 2: Parse presentation.xml to find <p:sldId> elements in order,
	// extracting their r:id attribute.
	var orderedIDs []string
	dec2 := xml.NewDecoder(bytes.NewReader(presXML))
	for {
		tok, err := dec2.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "sldId" {
			for _, attr := range se.Attr {
				// The r:id attribute uses the relationships namespace.
				// Match by local name "id" with a namespace containing "relationships".
				if attr.Name.Local == "id" && strings.Contains(attr.Name.Space, "relationships") {
					orderedIDs = append(orderedIDs, attr.Value)
					break
				}
			}
		}
	}

	if len(orderedIDs) == 0 {
		return nil
	}

	// Step 3: Resolve rIds to file paths.
	var result []string
	for _, rID := range orderedIDs {
		target, ok := rIdToTarget[rID]
		if !ok {
			return nil // incomplete mapping → fall back
		}
		result = append(result, target)
	}
	return result
}

// resolveNotesPath looks up the notes slide path for a given slide by parsing
// its relationship file (ppt/slides/_rels/slide{N}.xml.rels). It returns the
// resolved archive path (e.g. "ppt/notesSlides/notesSlide1.xml") or "" if no
// notes relationship is found.
func resolveNotesPath(fileIndex map[string]*zip.File, slideFileNum int) string {
	relsName := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", slideFileNum)
	relsData := readZipFile(fileIndex, relsName)
	if len(relsData) == 0 {
		return ""
	}

	const notesRelType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide"

	dec := xml.NewDecoder(bytes.NewReader(relsData))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "Relationship" {
			continue
		}
		var relType, target string
		for _, attr := range se.Attr {
			switch attr.Name.Local {
			case "Type":
				relType = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if relType == notesRelType && target != "" {
			// Targets are relative to ppt/slides/, e.g. "../notesSlides/notesSlide1.xml".
			// Normalize to an absolute archive path.
			target = strings.TrimPrefix(target, "/")
			if strings.HasPrefix(target, "..") {
				// "../notesSlides/X" → "ppt/notesSlides/X"
				for strings.HasPrefix(target, "../") {
					target = strings.TrimPrefix(target, "../")
				}
				target = path.Clean("ppt/" + target)
			} else if !strings.HasPrefix(target, "ppt/") {
				target = "ppt/slides/" + target
			}
			return target
		}
	}
	return ""
}

// Parse opens the .pptx file (a ZIP archive), reads slide XMLs in slide-number
// order, extracts all text from <a:t> elements, appends any speaker-note text,
// and returns the combined result with slides separated by form-feed characters.
func (p *PptxParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	zr, err := zip.OpenReader(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("pptx: open zip: %w", err)
	}
	defer zr.Close()

	// Index all files in the archive for fast lookup.
	fileIndex := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		fileIndex[f.Name] = f
	}

	// Try relationship-based ordering from presentation.xml first.
	presXML := readZipFile(fileIndex, "ppt/presentation.xml")
	relsXML := readZipFile(fileIndex, "ppt/_rels/presentation.xml.rels")
	xmlOrder := parseSlideOrderFromXML(presXML, relsXML)

	type numberedSlide struct {
		n       int    // display order (1-based)
		fileNum int    // actual slide number from filename (for notes lookup)
		name    string // archive path
	}
	var slides []numberedSlide

	if xmlOrder != nil {
		// Use relationship-based ordering.
		for i, path := range xmlOrder {
			if _, ok := fileIndex[path]; ok {
				// Extract the actual slide number from the filename
				// (e.g. "ppt/slides/slide3.xml" → 3) for notes lookup.
				fileNum := i + 1 // fallback to display order
				if m := slideRe.FindStringSubmatch(path); m != nil {
					if parsed, err := strconv.Atoi(m[1]); err == nil {
						fileNum = parsed
					}
				}
				slides = append(slides, numberedSlide{i + 1, fileNum, path})
			}
		}
	} else {
		// Fallback: collect slide file names and sort them numerically.
		for name := range fileIndex {
			if m := slideRe.FindStringSubmatch(name); m != nil {
				n, _ := strconv.Atoi(m[1])
				slides = append(slides, numberedSlide{n, n, name})
			}
		}
		slices.SortFunc(slides, func(a, b numberedSlide) int { return cmp.Compare(a.n, b.n) })
	}

	var parts []string
	for _, sl := range slides {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		slideText, err := extractTextFromZipFile(fileIndex[sl.name])
		if err != nil {
			return nil, fmt.Errorf("pptx: slide %d: %w", sl.n, err)
		}

		// Look for a corresponding notes slide. Prefer relationship-based
		// lookup from ppt/slides/_rels/slide{N}.xml.rels; fall back to
		// the filename heuristic using sl.fileNum.
		notesName := resolveNotesPath(fileIndex, sl.fileNum)
		if notesName == "" {
			notesName = fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", sl.fileNum)
		}
		var notesText string
		if nf, ok := fileIndex[notesName]; ok {
			notesText, err = extractTextFromZipFile(nf)
			if err != nil {
				return nil, fmt.Errorf("pptx: notes slide %d: %w", sl.n, err)
			}
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[Slide %d]\n", sl.n))
		sb.WriteString(slideText)
		sb.WriteString("\n[Speaker Notes]\n")
		sb.WriteString(notesText)
		sb.WriteByte('\f')
		parts = append(parts, sb.String())
	}

	text := strings.Join(parts, "\f")
	if len(text) > TextMaxChars {
		text = text[:TextMaxChars]
	}

	return &ParseResult{Text: text}, nil
}

// extractTextFromZipFile reads a zip.File and extracts text from all <a:t>
// elements, inserting newlines between <a:p> paragraph boundaries.
func extractTextFromZipFile(zf *zip.File) (string, error) {
	rc, err := zf.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return extractDrawingMLText(rc)
}

// extractDrawingMLText parses an XML reader using a token-based approach and
// collects text content from DrawingML <a:t> elements.  A newline is inserted
// between <a:p> paragraphs so that separate paragraphs are readable as distinct
// lines in the output.
func extractDrawingMLText(r io.Reader) (string, error) {
	const (
		nsDrawingML = "http://schemas.openxmlformats.org/drawingml/2006/main"
		localPara   = "p"
		localText   = "t"
	)

	dec := xml.NewDecoder(r)
	var sb strings.Builder
	var insideText bool
	firstPara := true

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("xml decode: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsDrawingML {
				switch t.Name.Local {
				case localPara:
					if !firstPara {
						sb.WriteByte('\n')
					}
					firstPara = false
				case localText:
					insideText = true
				}
			}
		case xml.EndElement:
			if t.Name.Space == nsDrawingML && t.Name.Local == localText {
				insideText = false
			}
		case xml.CharData:
			if insideText {
				sb.Write(t)
			}
		}
	}

	return sb.String(), nil
}
