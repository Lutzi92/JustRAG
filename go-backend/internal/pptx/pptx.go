// Package pptx creates minimal but valid PowerPoint (.pptx) files.
// A PPTX file is a ZIP archive containing Office Open XML parts.
package pptx

import (
	"archive/zip"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// Slide represents a single content slide with a title and bullet points.
type Slide struct {
	Title        string
	Points       []string
	SpeakerNotes string
}

// Presentation holds the data needed to generate a PPTX file.
type Presentation struct {
	Title    string
	Subtitle string
	Slides   []Slide
}

// WriteToFile creates a .pptx file at the given path. Parent directories are
// created automatically. The file contains a title slide followed by one
// content slide per entry in p.Slides.
func (p *Presentation) WriteToFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pptx: mkdir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("pptx: create file: %w", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	totalSlides := 1 + len(p.Slides) // title slide + content slides

	// Static parts
	if err := writeEntry(w, "[Content_Types].xml", contentTypes(totalSlides)); err != nil {
		return err
	}
	if err := writeEntry(w, "_rels/.rels", rootRels); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/presProps.xml", presProps); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/theme/theme1.xml", theme1); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/slideLayouts/slideLayout1.xml", slideLayout1); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/slideLayouts/_rels/slideLayout1.xml.rels", slideLayoutRels); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/slideMasters/slideMaster1.xml", slideMaster1(totalSlides)); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/slideMasters/_rels/slideMaster1.xml.rels", slideMasterRels); err != nil {
		return err
	}

	// Presentation part + rels
	if err := writeEntry(w, "ppt/presentation.xml", presentationXML(totalSlides)); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/_rels/presentation.xml.rels", presentationRels(totalSlides)); err != nil {
		return err
	}

	// Slide 1 — title slide
	if err := writeEntry(w, "ppt/slides/slide1.xml", titleSlideXML(p.Title, p.Subtitle)); err != nil {
		return err
	}
	if err := writeEntry(w, "ppt/slides/_rels/slide1.xml.rels", slideRels); err != nil {
		return err
	}

	// Content slides
	for i, s := range p.Slides {
		slideNum := i + 2 // slide numbering starts at 2
		name := fmt.Sprintf("ppt/slides/slide%d.xml", slideNum)
		relsName := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", slideNum)
		if err := writeEntry(w, name, contentSlideXML(s)); err != nil {
			return err
		}
		if err := writeEntry(w, relsName, slideRels); err != nil {
			return err
		}
	}

	return nil
}

// Markdown returns a readable text representation of the presentation.
func (p *Presentation) Markdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", p.Title)
	if p.Subtitle != "" {
		fmt.Fprintf(&sb, "*%s*\n\n", p.Subtitle)
	}
	for i, s := range p.Slides {
		fmt.Fprintf(&sb, "---\n\n## Slide %d: %s\n\n", i+1, s.Title)
		for _, pt := range s.Points {
			fmt.Fprintf(&sb, "- %s\n", pt)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func writeEntry(w *zip.Writer, name, content string) error {
	fw, err := w.Create(name)
	if err != nil {
		return fmt.Errorf("pptx: create entry %s: %w", name, err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		return fmt.Errorf("pptx: write entry %s: %w", name, err)
	}
	return nil
}

func esc(s string) string { return html.EscapeString(s) }

// EMU helpers — Office uses English Metric Units (1 inch = 914400 EMU).
const (
	emuInch = 914400
	slideW  = 10 * emuInch // 10 inches
	slideH  = 5625000      // 5.625 inches (16:9)
)

// ---------------------------------------------------------------------------
// XML templates — kept as Go string constants for zero dependencies.
// ---------------------------------------------------------------------------

func contentTypes(nSlides int) string {
	var slides strings.Builder
	for i := 1; i <= nSlides; i++ {
		fmt.Fprintf(&slides, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i)
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
` + slides.String() + `
</Types>`
}

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`

const presProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentationPr xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`

func presentationXML(nSlides int) string {
	var slides strings.Builder
	for i := 1; i <= nSlides; i++ {
		fmt.Fprintf(&slides, `<p:sldIdLst><p:sldId id="%d" r:id="rId%d"/></p:sldIdLst>`, 255+i, 10+i)
	}
	// Fix: sldIdLst should wrap all sldId entries
	var sldIds strings.Builder
	for i := 1; i <= nSlides; i++ {
		fmt.Fprintf(&sldIds, `<p:sldId id="%d" r:id="rId%d"/>`, 255+i, 10+i)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldIdLst>%s</p:sldIdLst>
<p:sldSz cx="%d" cy="%d"/>
<p:notesSz cx="%d" cy="%d"/>
</p:presentation>`, sldIds.String(), slideW, slideH, slideW, slideH)
}

func presentationRels(nSlides int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/presProps" Target="presProps.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>`)
	for i := 1; i <= nSlides; i++ {
		fmt.Fprintf(&sb, `
<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, 10+i, i)
	}
	sb.WriteString(`
</Relationships>`)
	return sb.String()
}

const slideRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`

const slideLayoutRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>
</Relationships>`

const slideLayout1 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="blank">
<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/></p:spTree></p:cSld></p:sldLayout>`

func slideMaster1(nSlides int) string {
	_ = nSlides
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/></p:spTree></p:cSld>
<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2"
accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>
</p:sldMaster>`
}

const slideMasterRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`

// theme1 is a minimal theme with corporate blue accent colours.
const theme1 = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="JustRAG">
<a:themeElements>
<a:clrScheme name="JustRAG">
<a:dk1><a:srgbClr val="000000"/></a:dk1>
<a:lt1><a:srgbClr val="FFFFFF"/></a:lt1>
<a:dk2><a:srgbClr val="363636"/></a:dk2>
<a:lt2><a:srgbClr val="F0F0F0"/></a:lt2>
<a:accent1><a:srgbClr val="165A97"/></a:accent1>
<a:accent2><a:srgbClr val="2D8F4E"/></a:accent2>
<a:accent3><a:srgbClr val="CC8400"/></a:accent3>
<a:accent4><a:srgbClr val="C0392B"/></a:accent4>
<a:accent5><a:srgbClr val="6A1B9A"/></a:accent5>
<a:accent6><a:srgbClr val="D84315"/></a:accent6>
<a:hlink><a:srgbClr val="165A97"/></a:hlink>
<a:folHlink><a:srgbClr val="6A1B9A"/></a:folHlink>
</a:clrScheme>
<a:fontScheme name="JustRAG">
<a:majorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont>
<a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont>
</a:fontScheme>
<a:fmtScheme name="JustRAG">
<a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst>
<a:lnStyleLst><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst>
<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>
<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst>
</a:fmtScheme>
</a:themeElements>
</a:theme>`

// titleSlideXML creates the XML for the first (title) slide.
func titleSlideXML(title, subtitle string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld>
<p:bg><p:bgPr><a:solidFill><a:srgbClr val="165A97"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp>
<p:nvSpPr><p:cNvPr id="2" name="Title"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>
<p:spPr>
<a:xfrm><a:off x="457200" y="1143000"/><a:ext cx="8229600" cy="1371600"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/>
</p:spPr>
<p:txBody>
<a:bodyPr anchor="ctr"/>
<a:lstStyle/>
<a:p><a:pPr algn="ctr"/><a:r><a:rPr lang="en-US" sz="3600" b="1" dirty="0"><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:latin typeface="Calibri"/></a:rPr><a:t>%s</a:t></a:r></a:p>
</p:txBody>
</p:sp>
<p:sp>
<p:nvSpPr><p:cNvPr id="3" name="Subtitle"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>
<p:spPr>
<a:xfrm><a:off x="457200" y="2743200"/><a:ext cx="8229600" cy="914400"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/>
</p:spPr>
<p:txBody>
<a:bodyPr anchor="ctr"/>
<a:lstStyle/>
<a:p><a:pPr algn="ctr"/><a:r><a:rPr lang="en-US" sz="1800" dirty="0"><a:solidFill><a:srgbClr val="E0E0E0"/></a:solidFill><a:latin typeface="Calibri"/></a:rPr><a:t>%s</a:t></a:r></a:p>
</p:txBody>
</p:sp>
</p:spTree>
</p:cSld>
</p:sld>`, esc(title), esc(subtitle))
}

// contentSlideXML creates the XML for a content slide with title + bullets.
func contentSlideXML(s Slide) string {
	var bullets strings.Builder
	for _, pt := range s.Points {
		fmt.Fprintf(&bullets, `<a:p><a:pPr marL="342900" indent="-342900"><a:buChar char="&#x2022;"/></a:pPr><a:r><a:rPr lang="en-US" sz="1800" dirty="0"><a:solidFill><a:srgbClr val="333333"/></a:solidFill><a:latin typeface="Calibri"/></a:rPr><a:t>%s</a:t></a:r></a:p>`, esc(pt))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>
<p:grpSpPr/>
<p:sp>
<p:nvSpPr><p:cNvPr id="2" name="Title"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>
<p:spPr>
<a:xfrm><a:off x="457200" y="274638"/><a:ext cx="8229600" cy="914400"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/>
</p:spPr>
<p:txBody>
<a:bodyPr anchor="b"/>
<a:lstStyle/>
<a:p><a:r><a:rPr lang="en-US" sz="2800" b="1" dirty="0"><a:solidFill><a:srgbClr val="165A97"/></a:solidFill><a:latin typeface="Calibri"/></a:rPr><a:t>%s</a:t></a:r></a:p>
</p:txBody>
</p:sp>
<p:sp>
<p:nvSpPr><p:cNvPr id="3" name="Content"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>
<p:spPr>
<a:xfrm><a:off x="457200" y="1371600"/><a:ext cx="8229600" cy="3657600"/></a:xfrm>
<a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/>
</p:spPr>
<p:txBody>
<a:bodyPr anchor="t"/>
<a:lstStyle/>
%s
</p:txBody>
</p:sp>
</p:spTree>
</p:cSld>
</p:sld>`, esc(s.Title), bullets.String())
}
