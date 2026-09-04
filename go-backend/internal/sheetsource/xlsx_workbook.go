package sheetsource

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var errPartMissing = errors.New("sheetsource: part not in archive")

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
	if rc, err := w.openPart("xl/sharedStrings.xml"); err != nil {
		if !errors.Is(err, errPartMissing) {
			zr.Close()
			return nil, fmt.Errorf("sheetsource: sharedStrings: %w", err)
		}
	} else {
		w.sst, err = parseSharedStrings(cappedPart(rc, "xl/sharedStrings.xml"))
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("sheetsource: sharedStrings: %w", err)
		}
	}
	if rc, err := w.openPart("xl/styles.xml"); err != nil {
		if !errors.Is(err, errPartMissing) {
			zr.Close()
			return nil, fmt.Errorf("sheetsource: styles: %w", err)
		}
	} else {
		w.xfs, err = parseStyles(cappedPart(rc, "xl/styles.xml"))
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
		return nil, errPartMissing
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("sheetsource: open part %q: %w", name, err)
	}
	return rc, nil
}

// sheetRowCount parses the `<dimension ref="A1:Z1234"/>` element that, per
// the OOXML schema, always precedes `<sheetData>` in a worksheet part. It
// stops scanning at the first of the two: a well-formed dimension yields
// the row count implied by its ref (a single-cell ref like "A1" yields 1);
// a missing dimension (or one this can't parse) yields 0. This lets a
// caller learn the sheet's row count WITHOUT reading the sheet body, unlike
// SheetExtras.RowCount which is only known after a full pass.
//
// Caveat seen in the wild: some writers never recompute the dimension, so
// a sheet with real data can still report a stale single-cell ref like
// "A1" (RowCount 1). This method reports exactly what the ref declares --
// downstream consumers that need a lower bound on the sheet's true extent
// must guard against a suspiciously small value themselves (see R58 /
// profile.RegionRows, which treats a declared count below the region's
// data start as unknown rather than trusting it).
func (w *xlsxWorkbook) sheetRowCount(part string) int {
	rc, err := w.openPart(part)
	if err != nil {
		return 0
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "dimension":
			rng, err := ParseRange(attr(se, "ref"))
			if err != nil {
				return 0
			}
			if n := rng.ToRow - rng.FromRow + 1; n > 0 {
				return n
			}
			return 0
		case "sheetData":
			// The dimension element always precedes sheetData; reaching
			// sheetData first means there was none to find.
			return 0
		}
	}
}

// parseWorkbook reads xl/workbook.xml (sheet list + state + date1904 +
// definedNames) and xl/_rels/workbook.xml.rels (r:id -> part path).
func (w *xlsxWorkbook) parseWorkbook() error {
	rels := map[string]string{}
	if rc, err := w.openPart("xl/_rels/workbook.xml.rels"); err != nil {
		if !errors.Is(err, errPartMissing) {
			return fmt.Errorf("sheetsource: workbook.xml.rels: %w", err)
		}
	} else {
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
