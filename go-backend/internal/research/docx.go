package research

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"strings"
)

// ---------------------------------------------------------------------------
// Minimal DOCX generation
//
// A DOCX file is a ZIP archive containing XML parts. This implementation
// produces a minimal but spec-compliant DOCX from a markdown string. It
// converts headings (# / ## / ###), bold (**text**), and plain paragraphs
// to WordprocessingML XML without any external dependencies.
// ---------------------------------------------------------------------------

// GenerateDocx converts a markdown report to a minimal DOCX byte slice.
func GenerateDocx(title, markdown string) []byte {
	bodyXML := markdownToWordXML(title, markdown)

	// Build the DOCX zip in memory.
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	addZipEntry(zw, "_rels/.rels", relsContent)
	addZipEntry(zw, "[Content_Types].xml", contentTypesXML)
	addZipEntry(zw, "word/_rels/document.xml.rels", documentRelsXML)
	addZipEntry(zw, "word/document.xml", buildDocumentXML(bodyXML))
	addZipEntry(zw, "word/styles.xml", stylesXML)

	zw.Close()
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// Markdown → WordprocessingML
// ---------------------------------------------------------------------------

func markdownToWordXML(title, md string) string {
	var sb strings.Builder

	// Title paragraph with Title style.
	if title != "" {
		sb.WriteString(para(title, "Title"))
	}

	lines := strings.Split(md, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, "### "):
			sb.WriteString(para(strings.TrimPrefix(line, "### "), "Heading3"))
		case strings.HasPrefix(line, "## "):
			sb.WriteString(para(strings.TrimPrefix(line, "## "), "Heading2"))
		case strings.HasPrefix(line, "# "):
			sb.WriteString(para(strings.TrimPrefix(line, "# "), "Heading1"))
		case strings.TrimSpace(line) == "":
			// blank line — skip (paragraph separation is implicit)
		default:
			// Plain paragraph — may contain **bold** runs.
			sb.WriteString(paraRuns(line, "Normal"))
		}

		i++
	}

	return sb.String()
}

// para produces a simple single-run paragraph with the given style.
func para(text, style string) string {
	return fmt.Sprintf(
		`<w:p><w:pPr><w:pStyle w:val="%s"/></w:pPr><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
		style, html.EscapeString(text),
	)
}

// paraRuns produces a paragraph with bold/normal runs, splitting on **...**.
func paraRuns(text, style string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:pStyle w:val="%s"/></w:pPr>`, style))

	parts := strings.Split(text, "**")
	for idx, part := range parts {
		if part == "" {
			continue
		}
		if idx%2 == 1 {
			// odd index → bold
			sb.WriteString(fmt.Sprintf(
				`<w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">%s</w:t></w:r>`,
				html.EscapeString(part),
			))
		} else {
			sb.WriteString(fmt.Sprintf(
				`<w:r><w:t xml:space="preserve">%s</w:t></w:r>`,
				html.EscapeString(part),
			))
		}
	}

	sb.WriteString(`</w:p>`)
	return sb.String()
}

// ---------------------------------------------------------------------------
// XML templates
// ---------------------------------------------------------------------------

func buildDocumentXML(bodyContent string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:wpc="http://schemas.microsoft.com/office/word/2010/wordprocessingCanvas"` +
		` xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"` +
		` xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"` +
		` mc:Ignorable="wpc">` +
		`<w:body>` +
		bodyContent +
		`<w:sectPr/>` +
		`</w:body>` +
		`</w:document>`
}

const relsContent = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`

const documentRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml" w:docDefaults="">
  <w:style w:type="paragraph" w:styleId="Normal">
    <w:name w:val="Normal"/>
    <w:rPr><w:sz w:val="24"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Title">
    <w:name w:val="Title"/>
    <w:pPr><w:jc w:val="center"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="52"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/>
    <w:rPr><w:b/><w:sz w:val="40"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading2">
    <w:name w:val="heading 2"/>
    <w:rPr><w:b/><w:sz w:val="32"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading3">
    <w:name w:val="heading 3"/>
    <w:rPr><w:b/><w:sz w:val="28"/></w:rPr>
  </w:style>
</w:styles>`

// ---------------------------------------------------------------------------
// Zip helper
// ---------------------------------------------------------------------------

func addZipEntry(zw *zip.Writer, name, content string) {
	w, err := zw.Create(name)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte(content))
}
