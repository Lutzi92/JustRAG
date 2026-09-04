package sheetsource

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	maxODSColRepeat = 16384
	// A repeated row group that carries content is materialised row by row,
	// so it stays capped: no real sheet repeats 1000 identical data rows,
	// and truncating there costs nothing downstream.
	maxODSRowRepeat = 1000
	// An empty repeat is only a gap, so it follows the xlsx reader's
	// maxGapRows instead: up to 100k, and an error beyond that rather than a
	// silent truncation, which would shift every row below the gap. The cap
	// is checked where the gap is flushed (i.e. only for gaps that actually
	// sit between content rows) — the trailing number-rows-repeated="1048566"
	// every LibreOffice file ends with is never delivered and never counted.
	maxODSEmptyRowRepeat = 100_000
)

// ODSSource reads OpenDocument Spreadsheet (.ods) files with a streaming
// xml.Decoder, two passes per read: OpenODS scans content.xml once for
// sheet names + hidden flags (cheap — table bodies are skipped), and
// ReadSheet re-opens content.xml and streams only the requested table.
type ODSSource struct {
	path   string
	sheets []odsSheetInfo
}

type odsSheetInfo struct {
	Name   string
	Hidden bool
}

var _ Source = (*ODSSource)(nil)

func OpenODS(path string) (src *ODSSource, err error) {
	defer recoverToErr(&err, "OpenODS")
	s := &ODSSource{path: path}
	if err := s.scanSheets(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ODSSource) Close() error { return nil }

func (s *ODSSource) Sheets() []SheetInfo {
	out := make([]SheetInfo, len(s.sheets))
	for i, sh := range s.sheets {
		out[i] = SheetInfo{Index: i, Name: sh.Name, Hidden: sh.Hidden}
	}
	return out
}

// openODSContent opens a fresh reader over content.xml inside the .ods zip
// archive. Each call reopens the archive from disk — OpenODS's first pass
// and every ReadSheet call get their own independent stream, per the
// two-pass design (no shared, stateful zip handle to reason about).
func openODSContent(path string) (*zip.ReadCloser, io.ReadCloser, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("sheetsource: open ods: %w", err)
	}
	var cf *zip.File
	for _, f := range zr.File {
		if strings.TrimPrefix(f.Name, "/") == "content.xml" {
			cf = f
			break
		}
	}
	if cf == nil {
		zr.Close()
		return nil, nil, fmt.Errorf("sheetsource: ods: content.xml not found in archive")
	}
	rc, err := cf.Open()
	if err != nil {
		zr.Close()
		return nil, nil, fmt.Errorf("sheetsource: ods: open content.xml: %w", err)
	}
	return zr, rc, nil
}

// scanSheets performs the first pass: table names + hidden flags only.
// Automatic table styles (name -> display) are collected while inside
// <office:automatic-styles>, which always precedes the body; each table's
// body is then skipped with decoder.Skip() since only its name and
// style-name are needed here.
func (s *ODSSource) scanSheets() error {
	zr, rc, err := openODSContent(s.path)
	if err != nil {
		return err
	}
	defer zr.Close()
	defer rc.Close()

	dec := xml.NewDecoder(cappedPart(rc, "content.xml"))
	hiddenStyles := map[string]bool{}
	inAutoStyles := false
	var curStyleName string
	curStyleIsTable := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("sheetsource: ods: content.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "automatic-styles":
				inAutoStyles = true
			case "style":
				if inAutoStyles {
					curStyleName = attr(t, "name")
					curStyleIsTable = attr(t, "family") == "table"
				}
			case "table-properties":
				if inAutoStyles && curStyleIsTable && attr(t, "display") == "false" {
					hiddenStyles[curStyleName] = true
				}
			case "table":
				name := attr(t, "name")
				styleName := attr(t, "style-name")
				s.sheets = append(s.sheets, odsSheetInfo{Name: name, Hidden: hiddenStyles[styleName]})
				if err := dec.Skip(); err != nil && err != io.EOF {
					return fmt.Errorf("sheetsource: ods: skip table %q: %w", name, err)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "automatic-styles" {
				inAutoStyles = false
			}
		}
	}
	if len(s.sheets) == 0 {
		return fmt.Errorf("sheetsource: ods: no sheets found")
	}
	return nil
}

// odsCell builds a Cell from a <table:table-cell>'s (namespace-stripped)
// attributes and its accumulated <text:p> text.
func odsCell(attrs map[string]string, text string) Cell {
	c := Cell{Kind: KindText, Raw: text}
	if f := attrs["formula"]; f != "" {
		c.IsFormula = true
		c.Formula = strings.TrimPrefix(strings.TrimPrefix(f, "of:"), "=")
	}
	switch attrs["value-type"] {
	case "float", "currency":
		if v, err := strconv.ParseFloat(attrs["value"], 64); err == nil {
			c.Kind, c.Raw = KindNumber, canonicalNumber(v)
		}
		c.Style.Unit = attrs["currency"]
	case "percentage":
		if v, err := strconv.ParseFloat(attrs["value"], 64); err == nil {
			c.Kind, c.Raw = KindNumber, percentRaw(v)
			c.Style.Percent = true
			c.Formatted = c.Raw + "%"
		}
	case "date":
		c.Kind = KindDate
		c.Raw = strings.TrimSuffix(attrs["date-value"], "T00:00:00")
	case "boolean":
		c.Kind = KindBool
		c.Raw = strings.ToLower(attrs["boolean-value"])
	case "time":
		c.Raw = attrs["time-value"]
	}
	if c.Kind == KindText && strings.TrimSpace(c.Raw) == "" && !c.IsFormula {
		c.Kind = KindEmpty
	}
	if c.Formatted == "" {
		c.Formatted = c.Raw
	}
	return c
}

// odsCellAttrs collects the subset of a <table:table-cell> start element's
// namespaced attributes that odsCell cares about.
func odsCellAttrs(t xml.StartElement) map[string]string {
	m := make(map[string]string, 6)
	for _, key := range []string{"value-type", "value", "date-value", "boolean-value", "time-value", "currency", "formula"} {
		if v := attr(t, key); v != "" {
			m[key] = v
		}
	}
	return m
}

func repeatCount(t xml.StartElement, name string, limit int) int {
	n := rawRepeatCount(t, name)
	if n > limit {
		n = limit
	}
	return n
}

// rawRepeatCount reads a *-repeated attribute without clamping it, so the
// caller can choose between truncating and refusing. Anything longer than
// nine digits is not a repeat count a spreadsheet can mean (and would
// overflow atoiAttr's accumulator), so it reads as "far too large".
func rawRepeatCount(t xml.StartElement, name string) int {
	if len(attr(t, name)) > 9 {
		return maxODSEmptyRowRepeat + 1
	}
	n := atoiAttr(t, name)
	if n < 1 {
		n = 1
	}
	return n
}

type odsValidationDef struct {
	ref    string
	values []string // nil unless the condition is an inline list
}

// parseODSInlineList recognises of:cell-content-is-in-list("a";"b";...) and
// returns its values. A range-based condition (e.g.
// cell-content-is-in-list([Sheet.$C$6:.$C$8])) or any other condition kind
// returns ok == false; per this task's scope, those are recorded with
// Values == nil and resolved in a follow-up.
func parseODSInlineList(cond string) (values []string, ok bool) {
	const prefix = "of:cell-content-is-in-list("
	if !strings.HasPrefix(cond, prefix) || !strings.HasSuffix(cond, ")") {
		return nil, false
	}
	inner := cond[len(prefix) : len(cond)-1]
	if !strings.HasPrefix(inner, `"`) {
		return nil, false
	}
	for _, part := range strings.Split(inner, ";") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, `"`)
		part = strings.TrimSuffix(part, `"`)
		values = append(values, part)
	}
	return values, true
}

// mergeSingleCellValidationRanges merges vertically adjacent single-cell
// ranges in the same column into one contiguous range, per validation.
func mergeSingleCellValidationRanges(ranges []Range) []Range {
	if len(ranges) == 0 {
		return nil
	}
	sorted := make([]Range, len(ranges))
	copy(sorted, ranges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FromCol != sorted[j].FromCol {
			return sorted[i].FromCol < sorted[j].FromCol
		}
		return sorted[i].FromRow < sorted[j].FromRow
	})
	var out []Range
	cur := sorted[0]
	for _, r := range sorted[1:] {
		if r.FromCol == cur.ToCol && r.FromRow == cur.ToRow+1 {
			cur.ToRow = r.ToRow
			continue
		}
		out = append(out, cur)
		cur = r
	}
	out = append(out, cur)
	return out
}

// ReadSheet performs the second pass: content.xml is reopened and streamed
// again; every <table:table> other than the requested index has its body
// skipped with decoder.Skip(). <table:content-validation> definitions are
// siblings of the tables (under office:spreadsheet) and are collected
// wherever encountered; cells' table:content-validation-name references are
// resolved against them once the whole document has been scanned.
func (s *ODSSource) ReadSheet(index int, fn RowFunc) (ex SheetExtras, err error) {
	defer recoverToErr(&err, "ODSSource.ReadSheet")
	if index < 0 || index >= len(s.sheets) {
		return ex, fmt.Errorf("sheetsource: sheet %d out of range", index)
	}

	zr, rc, err := openODSContent(s.path)
	if err != nil {
		return ex, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := xml.NewDecoder(cappedPart(rc, "content.xml"))

	defs := map[string]odsValidationDef{}
	usedRanges := map[string][]Range{}
	var usedOrder []string
	recordUsage := func(name string, row, col int) {
		if _, ok := usedRanges[name]; !ok {
			usedOrder = append(usedOrder, name)
		}
		usedRanges[name] = append(usedRanges[name], Range{row, col, row, col})
	}

	tableCount := -1
	inTarget := false

	// Row/cell parsing state, valid only while inTarget.
	var row []Cell
	rowIdx := 0
	pendingGaps := 0
	rowRepeat := 1
	rowHasContent := false
	lastGroupLen := 0
	lastGroupEmpty := false

	inCell := false
	cellIsCovered := false
	var cellAttrs map[string]string
	var cellText strings.Builder
	cellParagraphs := 0
	inP := false
	cellRepeat := 1
	cellColsSpanned := 0
	cellRowsSpanned := 0
	cellValidationName := ""
	groupColStart := 0

	deliver := func(idx int, cells []Cell) error {
		if len(cells) > ex.MaxCol {
			ex.MaxCol = len(cells)
		}
		return fn(idx, cells)
	}

	stoppedEarly := false

parse:
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ex, fmt.Errorf("sheetsource: ods: content.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "content-validation":
				name := attr(t, "name")
				cond := attr(t, "condition")
				def := odsValidationDef{ref: cond}
				if vals, ok := parseODSInlineList(cond); ok {
					def.values = vals
				}
				if name != "" {
					defs[name] = def
				}
				if err := dec.Skip(); err != nil && err != io.EOF {
					return ex, fmt.Errorf("sheetsource: ods: skip content-validation %q: %w", name, err)
				}
			case "table":
				tableCount++
				if tableCount != index {
					if err := dec.Skip(); err != nil && err != io.EOF {
						return ex, fmt.Errorf("sheetsource: ods: skip table: %w", err)
					}
					continue parse
				}
				inTarget = true
			case "table-row":
				if !inTarget {
					continue parse
				}
				row = nil
				// Uncapped here: whether this group is a gap or a content
				// run is only known at </table:table-row>, and the two get
				// different caps.
				rowRepeat = rawRepeatCount(t, "number-rows-repeated")
				rowHasContent = false
				lastGroupLen, lastGroupEmpty = 0, false
			case "table-cell", "covered-table-cell":
				if !inTarget {
					continue parse
				}
				inCell = true
				cellIsCovered = t.Name.Local == "covered-table-cell"
				cellText.Reset()
				cellParagraphs = 0
				groupColStart = len(row)
				cellRepeat = repeatCount(t, "number-columns-repeated", maxODSColRepeat)
				if cellIsCovered {
					cellAttrs, cellColsSpanned, cellRowsSpanned, cellValidationName = nil, 0, 0, ""
				} else {
					cellAttrs = odsCellAttrs(t)
					cellColsSpanned = atoiAttr(t, "number-columns-spanned")
					cellRowsSpanned = atoiAttr(t, "number-rows-spanned")
					cellValidationName = attr(t, "content-validation-name")
				}
			case "p":
				if inTarget && inCell {
					if cellParagraphs > 0 {
						cellText.WriteByte('\n')
					}
					cellParagraphs++
					inP = true
				}
			}
		case xml.CharData:
			if inTarget && inCell && inP {
				cellText.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				inP = false
			case "table-cell", "covered-table-cell":
				if !inTarget || !inCell {
					continue parse
				}
				var cell Cell
				if cellIsCovered {
					cell = Cell{}
				} else {
					cell = odsCell(cellAttrs, cellText.String())
					cs, rs := cellColsSpanned, cellRowsSpanned
					if cs < 1 {
						cs = 1
					}
					if rs < 1 {
						rs = 1
					}
					if cs > 1 || rs > 1 {
						ex.Merged = append(ex.Merged, Range{
							FromRow: rowIdx, FromCol: groupColStart,
							ToRow: rowIdx + rs - 1, ToCol: groupColStart + cs - 1,
						})
					}
				}
				empty := cell.IsEmpty() && !cell.IsFormula
				for i := 0; i < cellRepeat; i++ {
					row = append(row, cell)
					if cellValidationName != "" {
						recordUsage(cellValidationName, rowIdx, groupColStart+i)
					}
				}
				if !empty {
					rowHasContent = true
				}
				lastGroupLen, lastGroupEmpty = cellRepeat, empty
				inCell = false
			case "table-row":
				if !inTarget {
					continue parse
				}
				// Only the last cell group processed in this row is
				// eligible for trimming (LibreOffice's own padding block).
				// A row can have several trailing empty groups (e.g. a
				// small explicit blank group immediately followed by the
				// number-columns-repeated padding to 16384); trimming only
				// the final one preserves the earlier ones' column
				// positions instead of collapsing them too.
				if lastGroupEmpty && lastGroupLen > 0 && lastGroupLen <= len(row) {
					row = row[:len(row)-lastGroupLen]
				}
				if !rowHasContent {
					pendingGaps += rowRepeat
					continue parse
				}
				// Reached only when a content row follows, so this counts
				// interior gaps only — the trailing padding row every
				// LibreOffice file ends with is never flushed.
				if pendingGaps > maxODSEmptyRowRepeat {
					return ex, fmt.Errorf("sheetsource: ods: gap of %d empty rows before row %d", pendingGaps, rowIdx+1)
				}
				for pendingGaps > 0 {
					err := deliver(rowIdx, nil)
					if err != nil && !errors.Is(err, ErrStop) {
						return ex, err
					}
					// The row was delivered (fn ran, even if it then asked
					// to stop) — count it before advancing/breaking.
					ex.RowCount = rowIdx + 1
					stop := errors.Is(err, ErrStop)
					rowIdx++
					pendingGaps--
					if stop {
						stoppedEarly = true
						break parse
					}
				}
				contentRepeat := rowRepeat
				if contentRepeat > maxODSRowRepeat {
					contentRepeat = maxODSRowRepeat
				}
				for i := 0; i < contentRepeat; i++ {
					cp := make([]Cell, len(row))
					copy(cp, row)
					err := deliver(rowIdx, cp)
					if err != nil && !errors.Is(err, ErrStop) {
						return ex, err
					}
					ex.RowCount = rowIdx + 1
					stop := errors.Is(err, ErrStop)
					rowIdx++
					if stop {
						stoppedEarly = true
						break parse
					}
				}
			case "table":
				if inTarget {
					inTarget = false
				}
			}
		}
	}

	if stoppedEarly {
		return ex, nil
	}

	for _, name := range usedOrder {
		def, ok := defs[name]
		if !ok {
			continue
		}
		ex.Validations = append(ex.Validations, Validation{
			Sqref:  mergeSingleCellValidationRanges(usedRanges[name]),
			Ref:    def.ref,
			Values: def.values,
		})
	}

	return ex, nil
}
