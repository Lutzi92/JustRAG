package pptx

import (
	"archive/zip"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresentation_Markdown(t *testing.T) {
	t.Parallel()

	p := &Presentation{
		Title:    "Quarterly Review",
		Subtitle: "Q3 2026",
		Slides: []Slide{
			{Title: "Overview", Points: []string{"Revenue up", "Costs flat"}},
			{Title: "Outlook", Points: []string{"Hiring freeze", "New product"}},
		},
	}

	got := p.Markdown()

	wants := []string{
		"# Quarterly Review",
		"*Q3 2026*",
		"## Slide 1: Overview",
		"- Revenue up",
		"- Costs flat",
		"## Slide 2: Outlook",
		"- Hiring freeze",
		"- New product",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("Markdown() missing %q\n--- output ---\n%s", w, got)
		}
	}
}

func TestPresentation_Markdown_NoSubtitle(t *testing.T) {
	t.Parallel()

	p := &Presentation{Title: "Just a title"}
	got := p.Markdown()

	if !strings.Contains(got, "# Just a title") {
		t.Errorf("expected title heading, got: %q", got)
	}
	// Empty subtitle should not produce an italics line.
	if strings.Contains(got, "**\n") || strings.Contains(got, "*\n*") {
		t.Errorf("empty subtitle should be omitted, got: %q", got)
	}
}

func TestPresentation_WriteToFile_ProducesValidZip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.pptx")

	p := &Presentation{
		Title:    "Test Deck",
		Subtitle: "Subtitle",
		Slides: []Slide{
			{Title: "First", Points: []string{"A", "B"}},
			{Title: "Second", Points: []string{"C"}},
		},
	}
	if err := p.WriteToFile(path); err != nil {
		t.Fatalf("WriteToFile: %v", err)
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	have := make(map[string]bool, len(r.File))
	for _, f := range r.File {
		have[f.Name] = true
	}

	required := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"ppt/presentation.xml",
		"ppt/_rels/presentation.xml.rels",
		"ppt/presProps.xml",
		"ppt/theme/theme1.xml",
		"ppt/slideLayouts/slideLayout1.xml",
		"ppt/slideMasters/slideMaster1.xml",
		// title slide + 2 content slides
		"ppt/slides/slide1.xml",
		"ppt/slides/slide2.xml",
		"ppt/slides/slide3.xml",
		"ppt/slides/_rels/slide1.xml.rels",
		"ppt/slides/_rels/slide2.xml.rels",
		"ppt/slides/_rels/slide3.xml.rels",
	}
	for _, name := range required {
		if !have[name] {
			t.Errorf("zip missing required entry %q", name)
		}
	}
}

// TestPresentation_WriteToFile_EscapesXMLInjection locks in that user-supplied
// titles and bullet points cannot break out of the XML structure. A regression
// here would let any title containing "</a:t>" produce a malformed PPTX or
// inject arbitrary content into the rendered slide.
func TestPresentation_WriteToFile_EscapesXMLInjection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "evil.pptx")

	p := &Presentation{
		Title:    `</a:t><evil/>`,
		Subtitle: `R&D <ok>`,
		Slides: []Slide{
			{Title: `Slide "one"`, Points: []string{`a & b`, `c <x>`}},
		},
	}
	if err := p.WriteToFile(path); err != nil {
		t.Fatalf("WriteToFile: %v", err)
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "ppt/slides/slide") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := rc.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		_ = rc.Close()
		body := sb.String()

		// Raw user input must not appear unescaped — these would break the XML
		// out of the <a:t> text run.
		forbidden := []string{
			`</a:t><evil/>`,
			`<ok>`,
			`<x>`,
		}
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains unescaped %q", f.Name, bad)
			}
		}

		// Unescaped ampersand is invalid XML; only entities are allowed.
		// Replace known entities, then check for stray '&'.
		stripped := body
		for _, ent := range []string{"&amp;", "&lt;", "&gt;", "&quot;", "&#39;", "&#34;", "&#x2022;"} {
			stripped = strings.ReplaceAll(stripped, ent, "")
		}
		if strings.Contains(stripped, "&") {
			t.Errorf("%s contains unescaped '&'", f.Name)
		}
	}
}
