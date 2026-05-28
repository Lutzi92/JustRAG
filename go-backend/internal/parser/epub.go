package parser

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/html"
)

// EpubParser extracts text from EPUB files by reading the ZIP-based container,
// locating the OPF package document, walking the spine in reading order, and
// stripping HTML tags from each XHTML chapter.
type EpubParser struct{}

func (p *EpubParser) Name() string { return "EpubParser" }

// CanParse returns true for the application/epub+zip MIME type or .epub extension.
func (p *EpubParser) CanParse(mimeType, fileName string) bool {
	return mimeType == "application/epub+zip" || strings.HasSuffix(fileName, ".epub")
}

// Parse opens the EPUB at pctx.FilePath, resolves the reading order from the
// OPF spine, extracts text from each XHTML chapter, and joins them with double
// newlines. The result is truncated to TextMaxChars if necessary.
func (p *EpubParser) Parse(ctx context.Context, pctx ParseContext) (*ParseResult, error) {
	zr, err := zip.OpenReader(pctx.FilePath)
	if err != nil {
		return nil, fmt.Errorf("epub: open zip: %w", err)
	}
	defer zr.Close()

	// Build a lookup map from ZIP path → *zip.File for O(1) access.
	fileMap := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		fileMap[f.Name] = f
	}

	// Step 1: read META-INF/container.xml to find the OPF path.
	opfPath, err := readContainerXML(fileMap)
	if err != nil {
		return nil, fmt.Errorf("epub: container.xml: %w", err)
	}

	// Step 2: parse the OPF to get manifest (id → href) and spine order.
	opfDir := path.Dir(opfPath)
	manifest, spineIDs, err := readOPF(fileMap, opfPath)
	if err != nil {
		return nil, fmt.Errorf("epub: opf: %w", err)
	}

	// Step 3: extract and strip text from each spine chapter.
	var chapters []string
	for _, idref := range spineIDs {
		href, ok := manifest[idref]
		if !ok {
			continue
		}
		// Resolve the href relative to the OPF directory.
		chapterPath := resolveZipPath(opfDir, href)
		zf, ok := fileMap[chapterPath]
		if !ok {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("epub: open chapter %q: %w", chapterPath, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("epub: read chapter %q: %w", chapterPath, err)
		}

		text := stripHTML(string(data))
		text = strings.TrimSpace(text)
		if text != "" {
			chapters = append(chapters, text)
		}
	}

	text := strings.Join(chapters, "\n\n")
	if len(text) > TextMaxChars {
		text = text[:TextMaxChars]
	}
	return &ParseResult{Text: text}, nil
}

// ---- container.xml -------------------------------------------------------

type xmlContainer struct {
	XMLName   xml.Name      `xml:"container"`
	Rootfiles []xmlRootfile `xml:"rootfiles>rootfile"`
}

type xmlRootfile struct {
	FullPath  string `xml:"full-path,attr"`
	MediaType string `xml:"media-type,attr"`
}

func readContainerXML(fileMap map[string]*zip.File) (string, error) {
	zf, ok := fileMap["META-INF/container.xml"]
	if !ok {
		return "", fmt.Errorf("META-INF/container.xml not found")
	}
	rc, err := zf.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var container xmlContainer
	if err := xml.NewDecoder(rc).Decode(&container); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return "", fmt.Errorf("no rootfile entries found")
	}
	return container.Rootfiles[0].FullPath, nil
}

// ---- OPF -----------------------------------------------------------------

type xmlPackage struct {
	XMLName  xml.Name    `xml:"package"`
	Manifest xmlManifest `xml:"manifest"`
	Spine    xmlSpine    `xml:"spine"`
}

type xmlManifest struct {
	Items []xmlItem `xml:"item"`
}

type xmlItem struct {
	ID        string `xml:"id,attr"`
	Href      string `xml:"href,attr"`
	MediaType string `xml:"media-type,attr"`
}

type xmlSpine struct {
	Itemrefs []xmlItemref `xml:"itemref"`
}

type xmlItemref struct {
	Idref  string `xml:"idref,attr"`
	Linear string `xml:"linear,attr"`
}

// readOPF parses the OPF file and returns:
//   - manifest: map from item id → href (relative to OPF directory)
//   - spineIDs: item ids in reading order (linear items only, unless all are non-linear)
func readOPF(fileMap map[string]*zip.File, opfPath string) (manifest map[string]string, spineIDs []string, err error) {
	zf, ok := fileMap[opfPath]
	if !ok {
		return nil, nil, fmt.Errorf("OPF file %q not found in zip", opfPath)
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()

	var pkg xmlPackage
	if err := xml.NewDecoder(rc).Decode(&pkg); err != nil {
		return nil, nil, fmt.Errorf("decode: %w", err)
	}

	manifest = make(map[string]string, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		manifest[item.ID] = item.Href
	}

	for _, ref := range pkg.Spine.Itemrefs {
		// Skip explicitly non-linear items (e.g. cover image pages).
		if strings.EqualFold(ref.Linear, "no") {
			continue
		}
		spineIDs = append(spineIDs, ref.Idref)
	}

	return manifest, spineIDs, nil
}

// ---- helpers -------------------------------------------------------------

// resolveZipPath joins a base directory (from OPF location) with a relative
// href. ZIP paths use forward slashes; path.Join handles that correctly.
//
// hrefs in real-world EPUB OPF files are URL-encoded per the spec (e.g.
// "Section%20One.xhtml"), while ZIP entries are stored with the raw filename
// ("Section One.xhtml"). We URL-decode the href before resolving so chapter
// lookups don't silently miss files with spaces or unicode in their names.
func resolveZipPath(opfDir, href string) string {
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	if opfDir == "." || opfDir == "" {
		return href
	}
	return path.Join(opfDir, href)
}

// blockElements lists HTML tag names that should produce a newline boundary
// when encountered during text extraction.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"li": true, "tr": true, "blockquote": true, "pre": true,
	"section": true, "article": true, "header": true, "footer": true,
}

// skipElements lists tag names whose content should be completely ignored.
var skipElements = map[string]bool{
	"script": true, "style": true,
}

// stripHTML tokenizes htmlContent with golang.org/x/net/html and returns the
// plain-text content. Block-level elements are converted to newlines; script
// and style content is skipped entirely.
func stripHTML(htmlContent string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlContent))

	var sb strings.Builder
	skipDepth := 0 // depth inside a skip element (script/style)

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)

			if skipElements[tag] {
				if tt == html.StartTagToken {
					skipDepth++
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			if blockElements[tag] {
				sb.WriteByte('\n')
			}

		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)

			if skipElements[tag] {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			if blockElements[tag] {
				sb.WriteByte('\n')
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			sb.Write(tokenizer.Text())
		}
	}

	// Collapse runs of whitespace-only lines into a single blank line.
	lines := strings.Split(sb.String(), "\n")
	var out []string
	blankRun := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankRun++
			if blankRun == 1 {
				out = append(out, "")
			}
		} else {
			blankRun = 0
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
}
