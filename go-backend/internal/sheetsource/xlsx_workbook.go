package sheetsource

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

type xlsxSheetEntry struct {
	Name, Part string
	Hidden     bool
}

type xfInfo struct {
	NumFmtID                                  int
	Date, Percent, Bold, Filled, BottomBorder bool
	Unit                                      string
}

type xlsxWorkbook struct {
	zr           *zip.ReadCloser
	parts        map[string]*zip.File
	sheets       []xlsxSheetEntry
	date1904     bool
	definedNames map[string]string
	sst          []string
	xfs          []xfInfo
}

func openXLSX(filePath string) (*xlsxWorkbook, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("sheetsource: open xlsx: %w", err)
	}
	w := &xlsxWorkbook{zr: zr, parts: map[string]*zip.File{}, definedNames: map[string]string{}}
	for _, f := range zr.File {
		w.parts[strings.TrimPrefix(f.Name, "/")] = f
	}
	if err := w.parseWorkbook(); err != nil {
		zr.Close()
		return nil, err
	}
	if rc, err := w.openPart("xl/sharedStrings.xml"); err == nil {
		w.sst, err = parseSharedStrings(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("sheetsource: sharedStrings: %w", err)
		}
	}
	if rc, err := w.openPart("xl/styles.xml"); err == nil {
		w.xfs, err = parseStyles(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("sheetsource: styles: %w", err)
		}
	}
	return w, nil
}

func (w *xlsxWorkbook) Close() error { return w.zr.Close() }

func (w *xlsxWorkbook) openPart(name string) (io.ReadCloser, error) {
	f, ok := w.parts[name]
	if !ok {
		return nil, fmt.Errorf("sheetsource: part %q not in archive", name)
	}
	return f.Open()
}

// parseWorkbook reads xl/workbook.xml (sheet list + state + date1904 +
// definedNames) and xl/_rels/workbook.xml.rels (r:id -> part path).
func (w *xlsxWorkbook) parseWorkbook() error {
	rels := map[string]string{}
	if rc, err := w.openPart("xl/_rels/workbook.xml.rels"); err == nil {
		dec := xml.NewDecoder(rc)
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				target := attr(se, "Target")
				if strings.HasPrefix(target, "/") {
					target = strings.TrimPrefix(target, "/")
				} else {
					target = path.Join("xl", target)
				}
				rels[attr(se, "Id")] = target
			}
		}
		rc.Close()
	}
	rc, err := w.openPart("xl/workbook.xml")
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var curName string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("sheetsource: workbook.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "workbookPr":
				v := attr(t, "date1904")
				w.date1904 = v == "1" || v == "true"
			case "sheet":
				state := attr(t, "state")
				w.sheets = append(w.sheets, xlsxSheetEntry{
					Name:   attr(t, "name"),
					Part:   rels[attr(t, "id")],
					Hidden: state == "hidden" || state == "veryHidden",
				})
			case "definedName":
				curName = attr(t, "name")
			}
		case xml.CharData:
			if curName != "" {
				w.definedNames[curName] += string(t)
			}
		case xml.EndElement:
			if t.Name.Local == "definedName" {
				curName = ""
			}
		}
	}
	if len(w.sheets) == 0 {
		return fmt.Errorf("sheetsource: workbook has no sheets")
	}
	return nil
}

// parseSharedStrings concatenates every <t> inside each <si> (plain and
// rich-text runs), skipping phonetic <rPh> runs.
func parseSharedStrings(r io.Reader) ([]string, error) {
	dec := xml.NewDecoder(r)
	var out []string
	var cur strings.Builder
	inSI, inT, inRPh := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "rPh":
				inRPh = true
			case "t":
				inT = inSI && !inRPh
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "rPh":
				inRPh = false
			case "si":
				out = append(out, cur.String())
				inSI = false
			}
		}
	}
}
