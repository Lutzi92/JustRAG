package sheetsource

import (
	"encoding/xml"
	"io"

	"github.com/justrag/go-backend/internal/sheetsource/numfmt"
)

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
					d, p, u := numfmt.Classify(id, numFmts[id])
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
