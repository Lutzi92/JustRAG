package biffxls

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
	fNo := w.Xfs[xf].formatNo()
	switch {
	case fNo == 9 || fNo == 10:
		return false, true, ""
	case (fNo >= 14 && fNo <= 22) || (fNo >= 27 && fNo <= 36) || (fNo >= 45 && fNo <= 47) || (fNo >= 50 && fNo <= 58):
		return true, false, ""
	}
	if f := w.Formats[fNo]; f != nil {
		return classifyCustomFormat(f.str)
	}
	return false, false, ""
}

// DateMode1904 reports whether the workbook uses the 1904 date system.
func (w *WorkBook) DateMode1904() bool { return w.dateMode == 1 }
