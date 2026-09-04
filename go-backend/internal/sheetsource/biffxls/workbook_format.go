package biffxls

// FORK ADDITION (not upstream): number-format classification and the date-mode
// accessor. Upstream keeps Xfs/Formats/dateMode but exposes no way to ask what
// a cell's number format actually means.

// FormatIsDate classifies the number format bound to an XF index.
func (w *WorkBook) FormatIsDate(xf uint16) (date, percent bool) {
	if int(xf) >= len(w.Xfs) {
		return false, false
	}
	fNo := w.Xfs[xf].formatNo()
	switch {
	case fNo == 9 || fNo == 10:
		return false, true
	case (fNo >= 14 && fNo <= 22) || (fNo >= 27 && fNo <= 36) || (fNo >= 45 && fNo <= 47) || (fNo >= 50 && fNo <= 58):
		return true, false
	}
	if f := w.Formats[fNo]; f != nil {
		d, p, _ := classifyCustomFormat(f.str)
		return d, p
	}
	return false, false
}

// DateMode1904 reports whether the workbook uses the 1904 date system.
func (w *WorkBook) DateMode1904() bool { return w.dateMode == 1 }
