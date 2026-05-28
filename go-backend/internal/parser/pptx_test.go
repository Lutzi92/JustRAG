package parser

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// createTestPptx builds a minimal .pptx archive in a temp directory.
// Each element of slides becomes the text content of one slide's <a:t> element.
// If notesSlides is non-nil and has an entry for slide N (1-indexed), a
// corresponding notesSlide XML is written with that text.
func createTestPptx(t *testing.T, slides []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test pptx: %v", err)
	}
	w := zip.NewWriter(f)
	for i, text := range slides {
		fw, err := w.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i+1))
		if err != nil {
			t.Fatalf("zip create slide%d: %v", i+1, err)
		}
		fmt.Fprintf(fw, `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, text)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}
	return path
}

// createTestPptxWithNotes is like createTestPptx but also writes notesSlide XMLs.
// notes is a map from 1-based slide index to note text.
func createTestPptxWithNotes(t *testing.T, slides []string, notes map[int]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_notes.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test pptx: %v", err)
	}
	w := zip.NewWriter(f)

	for i, text := range slides {
		fw, err := w.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i+1))
		if err != nil {
			t.Fatalf("zip create slide%d: %v", i+1, err)
		}
		fmt.Fprintf(fw, `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, text)

		if noteText, ok := notes[i+1]; ok {
			nfw, err := w.Create(fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", i+1))
			if err != nil {
				t.Fatalf("zip create notesSlide%d: %v", i+1, err)
			}
			fmt.Fprintf(nfw, `<?xml version="1.0"?>
<p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
         xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:notes>`, noteText)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}
	return path
}

// createTestPptxOutOfOrder writes slides in reverse order inside the ZIP to
// verify that the parser sorts them numerically rather than by ZIP entry order.
func createTestPptxOutOfOrder(t *testing.T, slides []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outoforder.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create out-of-order pptx: %v", err)
	}
	w := zip.NewWriter(f)
	// Write slide entries in reverse order.
	for i := len(slides) - 1; i >= 0; i-- {
		fw, err := w.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i+1))
		if err != nil {
			t.Fatalf("zip create slide%d: %v", i+1, err)
		}
		fmt.Fprintf(fw, `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, slides[i])
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}
	return path
}

// ---------- parseSlideOrderFromXML tests ----------

func TestParseSlideOrderFromXML(t *testing.T) {
	t.Parallel()
	t.Run("order differs from numeric filename order", func(t *testing.T) {
		t.Parallel()
		// presentation.xml lists slides in order: rId3, rId1, rId2
		// which maps to slide3.xml, slide1.xml, slide2.xml
		presXML := []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldMasterIdLst>
    <p:sldMasterId id="2147483648" r:id="rId100"/>
  </p:sldMasterIdLst>
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId3"/>
    <p:sldId id="257" r:id="rId1"/>
    <p:sldId id="258" r:id="rId2"/>
  </p:sldIdLst>
</p:presentation>`)

		relsXML := []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide3.xml"/>
  <Relationship Id="rId100" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
</Relationships>`)

		order := parseSlideOrderFromXML(presXML, relsXML)
		if order == nil {
			t.Fatal("expected non-nil slide order")
		}
		expected := []string{
			"ppt/slides/slide3.xml",
			"ppt/slides/slide1.xml",
			"ppt/slides/slide2.xml",
		}
		if !slices.Equal(order, expected) {
			t.Fatalf("slides mismatch: got %v, want %v", order, expected)
		}
	})

	t.Run("nil presXML returns nil", func(t *testing.T) {
		t.Parallel()
		relsXML := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Target="slides/slide1.xml"/>
</Relationships>`)
		if order := parseSlideOrderFromXML(nil, relsXML); order != nil {
			t.Errorf("expected nil, got %v", order)
		}
	})

	t.Run("nil relsXML returns nil", func(t *testing.T) {
		t.Parallel()
		presXML := []byte(`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst>
</p:presentation>`)
		if order := parseSlideOrderFromXML(presXML, nil); order != nil {
			t.Errorf("expected nil, got %v", order)
		}
	})

	t.Run("empty XML returns nil", func(t *testing.T) {
		t.Parallel()
		if order := parseSlideOrderFromXML([]byte{}, []byte{}); order != nil {
			t.Errorf("expected nil, got %v", order)
		}
	})

	t.Run("malformed XML returns nil", func(t *testing.T) {
		t.Parallel()
		malformedPres := []byte(`<p:presentation><p:sldIdLst><p:sldId id="256" r:id="rId1"/>`)
		malformedRels := []byte(`<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"`)
		if order := parseSlideOrderFromXML(malformedPres, malformedRels); order != nil {
			t.Errorf("expected nil for malformed XML, got %v", order)
		}
		// Also check malformed presXML with valid relsXML.
		validRels := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Target="slides/slide1.xml"/>
</Relationships>`)
		if order := parseSlideOrderFromXML(malformedPres, validRels); order != nil {
			t.Errorf("expected nil for malformed presXML with valid rels, got %v", order)
		}
	})

	t.Run("missing rId in rels returns nil", func(t *testing.T) {
		t.Parallel()
		presXML := []byte(`<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId1"/>
    <p:sldId id="257" r:id="rId99"/>
  </p:sldIdLst>
</p:presentation>`)
		relsXML := []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Target="slides/slide1.xml"/>
</Relationships>`)
		if order := parseSlideOrderFromXML(presXML, relsXML); order != nil {
			t.Errorf("expected nil for incomplete mapping, got %v", order)
		}
	})
}

// ---------- CanParse tests ----------

func TestPptxParserCanParse(t *testing.T) {
	t.Parallel()
	p := &PptxParser{}
	cases := []struct {
		mime     string
		fileName string
		want     bool
	}{
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", "deck.pptx", true},
		{"application/octet-stream", "deck.pptx", true},
		{"application/octet-stream", "deck.PPTX", true},
		{"application/pdf", "deck.pdf", false},
		{"text/plain", "notes.txt", false},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", "noextension", true},
	}
	for _, tc := range cases {
		got := p.CanParse(tc.mime, tc.fileName)
		if got != tc.want {
			t.Errorf("CanParse(%q, %q) = %v, want %v", tc.mime, tc.fileName, got, tc.want)
		}
	}
}

// ---------- Parse tests ----------

func TestPptxParserTwoSlides(t *testing.T) {
	t.Parallel()
	path := createTestPptx(t, []string{"Hello World", "Second Slide"})

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text

	if !strings.Contains(text, "[Slide 1]") {
		t.Error("expected [Slide 1] header")
	}
	if !strings.Contains(text, "[Slide 2]") {
		t.Error("expected [Slide 2] header")
	}
	if !strings.Contains(text, "Hello World") {
		t.Error("expected slide 1 text")
	}
	if !strings.Contains(text, "Second Slide") {
		t.Error("expected slide 2 text")
	}

	// Slides must be separated by at least one form-feed.
	if !strings.Contains(text, "\f") {
		t.Error("expected form-feed separator between slides")
	}

	// [Slide 1] must appear before [Slide 2] in the output.
	idx1 := strings.Index(text, "[Slide 1]")
	idx2 := strings.Index(text, "[Slide 2]")
	if idx1 >= idx2 {
		t.Errorf("[Slide 1] (pos %d) should appear before [Slide 2] (pos %d)", idx1, idx2)
	}
}

func TestPptxParserSlidesContainSpeakerNotesSection(t *testing.T) {
	t.Parallel()
	path := createTestPptx(t, []string{"Intro"})

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Text, "[Speaker Notes]") {
		t.Error("expected [Speaker Notes] section in output")
	}
}

func TestPptxParserSpeakerNotesText(t *testing.T) {
	t.Parallel()
	notes := map[int]string{1: "Remember to pause here", 2: "Questions and answers time"}
	path := createTestPptxWithNotes(t, []string{"Title Slide", "Agenda"}, notes)

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test_notes.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	if !strings.Contains(text, "Remember to pause here") {
		t.Error("expected speaker note text for slide 1")
	}
	if !strings.Contains(text, "Questions and answers time") {
		t.Error("expected speaker note text for slide 2")
	}
}

func TestPptxParserNumericalSlideOrder(t *testing.T) {
	// Slides are stored in reverse order inside the ZIP; the parser must still
	// emit them in slide-number order (1, 2, 3).
	t.Parallel()
	slideTexts := []string{"First", "Second", "Third"}
	path := createTestPptxOutOfOrder(t, slideTexts)

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "outoforder.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	idx1 := strings.Index(text, "First")
	idx2 := strings.Index(text, "Second")
	idx3 := strings.Index(text, "Third")

	if idx1 < 0 || idx2 < 0 || idx3 < 0 {
		t.Fatalf("one or more slide texts missing from output")
	}
	if !(idx1 < idx2 && idx2 < idx3) {
		t.Errorf("slides not in numerical order: First@%d Second@%d Third@%d", idx1, idx2, idx3)
	}
}

func TestPptxParserEmptyPresentation(t *testing.T) {
	// A valid ZIP with no slide files → empty result.
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create empty pptx: %v", err)
	}
	w := zip.NewWriter(f)
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "empty.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "" {
		t.Errorf("expected empty text for empty presentation, got %q", result.Text)
	}
}

func TestPptxParserMultipleParagraphsPerSlide(t *testing.T) {
	// Build a slide with two <a:p> paragraphs to verify they are separated by newlines.
	t.Parallel()
	slideXML := `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>Paragraph One</a:t></a:r></a:p>
<a:p><a:r><a:t>Paragraph Two</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`

	path := filepath.Join(t.TempDir(), "multi_para.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := zip.NewWriter(f)
	fw, err := w.Create("ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	fmt.Fprint(fw, slideXML)
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "multi_para.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Text
	if !strings.Contains(text, "Paragraph One") {
		t.Error("expected 'Paragraph One' in output")
	}
	if !strings.Contains(text, "Paragraph Two") {
		t.Error("expected 'Paragraph Two' in output")
	}
	// The two paragraphs must be separated by at least one newline.
	idx1 := strings.Index(text, "Paragraph One")
	idx2 := strings.Index(text, "Paragraph Two")
	between := text[idx1+len("Paragraph One") : idx2]
	if !strings.Contains(between, "\n") {
		t.Errorf("expected newline between paragraphs, got %q", between)
	}
}

// createTestPptxWithNotesRels builds a .pptx where notes are linked via
// slide relationship files (ppt/slides/_rels/slide{N}.xml.rels) rather than
// relying on the filename convention. If includeRels is true, the rels files
// are written; otherwise only the notesSlide XMLs are present (fallback path).
func createTestPptxWithNotesRels(t *testing.T, slides []string, notes map[int]string, includeRels bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_notes_rels.pptx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test pptx: %v", err)
	}
	w := zip.NewWriter(f)

	for i, text := range slides {
		slideNum := i + 1
		fw, err := w.Create(fmt.Sprintf("ppt/slides/slide%d.xml", slideNum))
		if err != nil {
			t.Fatalf("zip create slide%d: %v", slideNum, err)
		}
		fmt.Fprintf(fw, `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, text)

		if noteText, ok := notes[slideNum]; ok {
			// Use a non-standard notes filename to ensure rel-based lookup works.
			notesFile := fmt.Sprintf("ppt/notesSlides/notes_%d.xml", slideNum)
			nfw, err := w.Create(notesFile)
			if err != nil {
				t.Fatalf("zip create notes_%d: %v", slideNum, err)
			}
			fmt.Fprintf(nfw, `<?xml version="1.0"?>
<p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
         xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
<p:cSld><p:spTree><p:sp><p:txBody>
<a:p><a:r><a:t>%s</a:t></a:r></a:p>
</p:txBody></p:sp></p:spTree></p:cSld></p:notes>`, noteText)

			if includeRels {
				relsPath := fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", slideNum)
				rfw, err := w.Create(relsPath)
				if err != nil {
					t.Fatalf("zip create rels for slide%d: %v", slideNum, err)
				}
				fmt.Fprintf(rfw, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notes_%d.xml"/>
</Relationships>`, slideNum)
			}
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}
	return path
}

func TestPptxParserNotesViaRelationship(t *testing.T) {
	// Notes files use non-standard names (notes_1.xml instead of notesSlide1.xml)
	// so only the relationship-based lookup can find them.
	t.Parallel()
	notes := map[int]string{1: "Rel-based note for slide 1"}
	path := createTestPptxWithNotesRels(t, []string{"Slide One"}, notes, true)

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "Rel-based note for slide 1") {
		t.Error("expected notes text resolved via relationship file")
	}
}

func TestPptxParserNotesFallbackWithoutRels(t *testing.T) {
	// Standard notes filenames (notesSlide{N}.xml) without rels → fallback path.
	t.Parallel()
	notes := map[int]string{1: "Fallback note"}
	path := createTestPptxWithNotes(t, []string{"Slide One"}, notes)

	p := &PptxParser{}
	result, err := p.Parse(context.Background(), ParseContext{
		FilePath: path,
		FileName: "test.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "Fallback note") {
		t.Error("expected notes text resolved via filename fallback")
	}
}

func TestResolveNotesPath(t *testing.T) {
	t.Parallel()
	t.Run("valid rels with notes relationship", func(t *testing.T) {
		t.Parallel()
		// Build a minimal zip with just the rels file.
		path := filepath.Join(t.TempDir(), "rels.pptx")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := zip.NewWriter(f)
		fw, err := w.Create("ppt/slides/_rels/slide1.xml.rels")
		if err != nil {
			t.Fatalf("zip create rels: %v", err)
		}
		if _, err := fmt.Fprint(fw, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`); err != nil {
			t.Fatalf("write rels XML: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("file close: %v", err)
		}

		zr, err := zip.OpenReader(path)
		if err != nil {
			t.Fatalf("open zip reader: %v", err)
		}
		defer zr.Close()
		idx := make(map[string]*zip.File)
		for _, zf := range zr.File {
			idx[zf.Name] = zf
		}

		got := resolveNotesPath(idx, 1)
		if got != "ppt/notesSlides/notesSlide1.xml" {
			t.Errorf("got %q, want ppt/notesSlides/notesSlide1.xml", got)
		}
	})

	t.Run("no rels file returns empty", func(t *testing.T) {
		t.Parallel()
		idx := map[string]*zip.File{}
		got := resolveNotesPath(idx, 1)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("rels without notes relationship returns empty", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "no_notes_rels.pptx")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := zip.NewWriter(f)
		fw, err := w.Create("ppt/slides/_rels/slide1.xml.rels")
		if err != nil {
			t.Fatalf("zip create rels: %v", err)
		}
		if _, err := fmt.Fprint(fw, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`); err != nil {
			t.Fatalf("write rels XML: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("file close: %v", err)
		}

		zr, err := zip.OpenReader(path)
		if err != nil {
			t.Fatalf("open zip reader: %v", err)
		}
		defer zr.Close()
		idx := make(map[string]*zip.File)
		for _, zf := range zr.File {
			idx[zf.Name] = zf
		}

		got := resolveNotesPath(idx, 1)
		if got != "" {
			t.Errorf("expected empty for rels without notes, got %q", got)
		}
	})
}
