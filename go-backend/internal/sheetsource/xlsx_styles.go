package sheetsource

import (
	"encoding/xml"
	"io"
	"strings"
)

var builtinDateFmts = map[int]bool{14: true, 15: true, 16: true, 17: true, 18: true, 19: true, 20: true, 21: true, 22: true,
	27: true, 28: true, 29: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true, 36: true,
	45: true, 46: true, 47: true, 50: true, 51: true, 52: true, 53: true, 54: true, 55: true, 56: true, 57: true, 58: true}

func classifyNumFmt(id int, code string) (date, percent bool, unit string) {
	if code == "" {
		return builtinDateFmts[id], id == 9 || id == 10, ""
	}
	var rest, units strings.Builder
	for i := 0; i < len(code); i++ {
		switch c := code[i]; c {
		case '"':
			j := strings.IndexByte(code[i+1:], '"')
			if j < 0 {
				j = len(code) - i - 1
			}
			lit := strings.TrimSpace(code[i+1 : i+1+j])
			if lit != "" {
				units.WriteString(lit)
			}
			i += j + 1
		case '[':
			j := strings.IndexByte(code[i:], ']')
			if j < 0 {
				j = len(code) - i - 1
			}
			sec := code[i+1 : i+j]
			if strings.HasPrefix(sec, "$") { // [$€-407] currency section
				cur, _, _ := strings.Cut(sec[1:], "-")
				if cur = strings.TrimSpace(cur); cur != "" {
					units.WriteString(cur)
				}
			}
			i += j
		case '\\':
			if i+1 < len(code) {
				e := code[i+1]
				if e != ' ' && (e < '0' || e > '9') {
					units.WriteByte(e)
				}
				i++
			}
		default:
			rest.WriteByte(c)
		}
	}
	r := strings.ToLower(rest.String())
	if strings.TrimSpace(r) == "general" {
		return false, false, strings.TrimSpace(units.String())
	}
	percent = strings.Contains(r, "%")
	date = strings.ContainsAny(r, "ydhs") || (strings.Contains(r, "m") && !strings.ContainsAny(r, "0#?"))
	return date, percent, strings.TrimSpace(units.String())
}

// parseStyles reads xl/styles.xml into w.xfs. Only the attributes the
// profiler needs are kept; everything else is skipped.
func parseStyles(r io.Reader) ([]xfInfo, error) {
	dec := xml.NewDecoder(r)
	numFmts := map[int]string{}
	var fontsBold []bool
	var fillsSolid []bool
	var bordersBottom []bool
	var xfs []xfInfo
	section := ""
	inFont, inFill, inBorder := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return xfs, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmts", "fonts", "fills", "borders", "cellXfs", "cellStyleXfs", "dxfs":
				section = t.Name.Local
			case "numFmt":
				if section == "numFmts" {
					numFmts[atoiAttr(t, "numFmtId")] = attr(t, "formatCode")
				}
			case "font":
				if section == "fonts" {
					inFont = true
					fontsBold = append(fontsBold, false)
				}
			case "b":
				if inFont && attr(t, "val") != "0" && attr(t, "val") != "false" {
					fontsBold[len(fontsBold)-1] = true
				}
			case "fill":
				if section == "fills" {
					inFill = true
					fillsSolid = append(fillsSolid, false)
				}
			case "patternFill":
				if inFill {
					pt := attr(t, "patternType")
					fillsSolid[len(fillsSolid)-1] = pt != "" && pt != "none"
				}
			case "border":
				if section == "borders" {
					inBorder = true
					bordersBottom = append(bordersBottom, false)
				}
			case "bottom":
				if inBorder && attr(t, "style") != "" && attr(t, "style") != "none" {
					bordersBottom[len(bordersBottom)-1] = true
				}
			case "xf":
				if section == "cellXfs" {
					id := atoiAttr(t, "numFmtId")
					d, p, u := classifyNumFmt(id, numFmts[id])
					x := xfInfo{NumFmtID: id, Date: d, Percent: p, Unit: u}
					if fi := atoiAttr(t, "fontId"); fi < len(fontsBold) {
						x.Bold = fontsBold[fi]
					}
					if fi := atoiAttr(t, "fillId"); fi < len(fillsSolid) {
						x.Filled = fillsSolid[fi]
					}
					if bi := atoiAttr(t, "borderId"); bi < len(bordersBottom) {
						x.BottomBorder = bordersBottom[bi]
					}
					xfs = append(xfs, x)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "font":
				inFont = false
			case "fill":
				inFill = false
			case "border":
				inBorder = false
			case "numFmts", "fonts", "fills", "borders", "cellXfs", "cellStyleXfs", "dxfs":
				section = ""
			}
		}
	}
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func atoiAttr(e xml.StartElement, name string) int {
	n := 0
	for _, c := range attr(e, name) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
