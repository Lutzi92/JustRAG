package biffxls

import "github.com/justrag/go-backend/internal/sheetsource/numfmt"

// FORK ADDITION (not upstream): number-format classification and the date-mode
// accessor. Upstream keeps Xfs/Formats/dateMode but exposes no way to ask what
// a cell's number format actually means.

// FormatIsDate classifies the number format bound to an XF index.
func (w *WorkBook) FormatIsDate(xf uint16) (date, percent bool) {
	d, p, _ := w.FormatInfo(xf)
	return d, p
}

// FormatInfo classifies the number format bound to an XF index and reports the
// literal unit carried by the format code ("€", "m²", …), empty when there is
// none. The profiler uses the unit to label a column.
func (w *WorkBook) FormatInfo(xf uint16) (date, percent bool, unit string) {
	if int(xf) >= len(w.Xfs) {
		return false, false, ""
	}
	fNo := int(w.Xfs[xf].formatNo())
	// A built-in id wins over any format code recorded for it, matching the
	// pre-fork behaviour; everything else is classified from its code.
	if numfmt.IsBuiltin(fNo) {
		return numfmt.Classify(fNo, "")
	}
	if f := w.Formats[uint16(fNo)]; f != nil {
		return numfmt.Classify(fNo, f.str)
	}
	return false, false, ""
}

// DateMode1904 reports whether the workbook uses the 1904 date system.
func (w *WorkBook) DateMode1904() bool { return w.dateMode == 1 }
