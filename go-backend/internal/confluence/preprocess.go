package confluence

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// acDrawioRe matches a Confluence draw.io plugin macro. Captures the full
// macro block and (separately, via acParameterRe) parameters. Example:
//
//	<ac:structured-macro ac:name="drawio" ...>
//	  <ac:parameter ac:name="diagramName">versuch01</ac:parameter>
//	  ...
//	</ac:structured-macro>
var acDrawioRe = regexp.MustCompile(
	`(?s)<ac:structured-macro\s+[^>]*ac:name="drawio"[^>]*>.*?</ac:structured-macro>`,
)

// acDiagramNameRe extracts the diagramName parameter from inside a drawio
// macro block.
var acDiagramNameRe = regexp.MustCompile(
	`(?s)<ac:parameter\s+ac:name="diagramName"\s*>([^<]*)</ac:parameter>`,
)

// mxValueRe finds value="..." on mxCell and label="..." on object elements
// inside a drawio XML file. Used as a parser fallback when encoding/xml
// chokes on broken XML (drawio's plugin sometimes stores slightly malformed
// content).
var mxValueRe = regexp.MustCompile(`<(?:mxCell|object)\b[^>]*\s(?:value|label)="([^"]*)"`)

// drawioPreferredExts orders attachment filenames when a diagramName has
// multiple matching attachments (draw.io stores the source + rendered
// previews). The .drawio/ bare source has the richest labels; fall back
// through SVG, then PNG.
var drawioPreferredExts = []string{"", ".drawio", ".xml", ".svg"}

// extractDrawioText returns a comma-separated list of labels found inside a
// drawio file (mxfile / mxGraphModel). Handles both mxCell @value and
// object @label. Falls through to extractSVGText if the root element is
// <svg> (drawio's SVG export contains labels as <text> nodes).
func extractDrawioText(data []byte) string {
	// Cheap sniff for the <svg> root so SVG exports use the dedicated walker.
	if bytes.Contains(data[:min(len(data), 256)], []byte("<svg")) &&
		!bytes.Contains(data[:min(len(data), 256)], []byte("<mxfile")) {
		return extractSVGText(data)
	}

	labels := parseDrawioValuesXML(data)
	if len(labels) == 0 {
		// Broken XML: fall back to regex scrape.
		labels = parseDrawioValuesRegex(data)
	}
	return strings.Join(labels, ", ")
}

// parseDrawioValuesXML walks the XML tree and collects value/label attrs on
// mxCell and object elements. Returns the decoded strings (HTML entities
// already unescaped by encoding/xml).
func parseDrawioValuesXML(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false

	var labels []string
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(se.Name.Local)
		if name != "mxcell" && name != "object" {
			continue
		}
		attrName := "value"
		if name == "object" {
			attrName = "label"
		}
		for _, a := range se.Attr {
			if strings.ToLower(a.Name.Local) == attrName && a.Value != "" {
				text := strings.Join(strings.Fields(a.Value), " ")
				if text != "" {
					labels = append(labels, text)
				}
				break
			}
		}
	}
	return labels
}

// parseDrawioValuesRegex is the regex fallback when XML parsing fails.
func parseDrawioValuesRegex(data []byte) []string {
	matches := mxValueRe.FindAllSubmatch(data, -1)
	labels := make([]string, 0, len(matches))
	for _, m := range matches {
		text := strings.Join(strings.Fields(xmlUnescape(string(m[1]))), " ")
		if text != "" {
			labels = append(labels, text)
		}
	}
	return labels
}

// xmlUnescape is a minimal XML-entity unescaper covering the entities
// drawio commonly produces.
var entityReplacer = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&apos;", "'",
	"&#xa;", "\n",
	"&#10;", "\n",
)

func xmlUnescape(s string) string { return entityReplacer.Replace(s) }

// processDrawioMacros replaces every <ac:structured-macro ac:name="drawio">
// with an "[Diagram: name]" placeholder, optionally followed by a paragraph
// of labels extracted from the matched attachment. Attachments are
// discovered by matching the diagramName parameter against attachment
// titles, preferring the source file over rendered previews.
//
// Runs before the acTagRe stripper in confluenceHTMLToMarkdown so the
// replacement survives into the final markdown.
func processDrawioMacros(ctx context.Context, html string, fetcher inlineImageFetcher, pageID string) string {
	matches := acDrawioRe.FindAllStringIndex(html, -1)
	if len(matches) == 0 {
		return html
	}

	// Collect each macro + its diagramName once, in document order.
	type macro struct {
		start, end int
		name       string // diagramName, "" if missing
	}
	macros := make([]macro, 0, len(matches))
	for _, loc := range matches {
		block := html[loc[0]:loc[1]]
		var diagramName string
		if sub := acDiagramNameRe.FindStringSubmatch(block); sub != nil {
			diagramName = strings.TrimSpace(sub[1])
		}
		macros = append(macros, macro{start: loc[0], end: loc[1], name: diagramName})
	}

	// Resolve diagramName → labels once per name (cache).
	cache := map[string]string{}
	var attachmentLinks map[string]string

	loadAttachments := func() map[string]string {
		if attachmentLinks != nil {
			return attachmentLinks
		}
		attachmentLinks = map[string]string{} // non-nil = "already tried"
		atts, err := fetcher.GetPageAttachments(ctx, pageID)
		if err != nil {
			slog.Warn("confluence: failed to list attachments for drawio macros", "pageId", pageID, "error", err)
			return attachmentLinks
		}
		for _, a := range atts {
			attachmentLinks[a.Title] = a.DownloadLink
		}
		return attachmentLinks
	}

	for _, m := range macros {
		if m.name == "" {
			continue
		}
		if _, seen := cache[m.name]; seen {
			continue
		}
		links := loadAttachments()
		link := pickDrawioAttachment(m.name, links)
		if link == "" {
			slog.Debug("confluence: drawio attachment not found", "pageId", pageID, "diagramName", m.name)
			cache[m.name] = ""
			continue
		}
		data, err := fetcher.DownloadAttachment(ctx, link)
		if err != nil {
			slog.Warn("confluence: drawio attachment download failed", "pageId", pageID, "diagramName", m.name, "error", err)
			cache[m.name] = ""
			continue
		}
		cache[m.name] = extractDrawioText(data)
	}

	// Replace in reverse so offsets stay valid.
	for i := len(macros) - 1; i >= 0; i-- {
		m := macros[i]
		name := m.name
		if name == "" {
			name = "diagram"
		}
		replacement := fmt.Sprintf(`<p><strong>[Diagram: %s]</strong></p>`, name)
		if labels := cache[m.name]; labels != "" {
			replacement += fmt.Sprintf(`<p>%s</p>`, labels)
		}
		html = html[:m.start] + replacement + html[m.end:]
	}
	return html
}

// pickDrawioAttachment returns the best download link for the diagramName or
// "" if nothing matches. Prefers the bare source file over rendered previews.
func pickDrawioAttachment(diagramName string, links map[string]string) string {
	for _, ext := range drawioPreferredExts {
		if link, ok := links[diagramName+ext]; ok {
			return link
		}
	}
	// Last-ditch: any attachment whose title starts with diagramName + "."
	for title, link := range links {
		if strings.HasPrefix(title, diagramName+".") {
			return link
		}
	}
	return ""
}

// extractSVGText walks an SVG file and returns a comma-separated list of
// label-like text drawn from <title>, <desc>, and <text> elements. Returns
// "" if the input isn't parseable SVG or contains no text.
//
// RAG motivation: architecture diagrams in Confluence are frequently SVG,
// and their <text> labels ("API Gateway", "PostgreSQL", …) are often the
// most retrieval-valuable part of the page. Extracting them gives the
// chunker something to embed beyond just the filename placeholder.
//
// <tspan> and other inline children inside <text> are concatenated into the
// parent text so "User Service" reads as one label rather than two.
func extractSVGText(data []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false

	var labels []string
	depth := 0 // >0 means we're inside a tracked element
	var buf strings.Builder

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if depth > 0 {
				depth++
				continue
			}
			if name == "title" || name == "desc" || name == "text" {
				depth = 1
				buf.Reset()
			}
		case xml.CharData:
			if depth > 0 {
				buf.Write(t)
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
				if depth == 0 {
					text := strings.Join(strings.Fields(buf.String()), " ")
					if text != "" {
						labels = append(labels, text)
					}
				}
			}
		}
	}
	return strings.Join(labels, ", ")
}
