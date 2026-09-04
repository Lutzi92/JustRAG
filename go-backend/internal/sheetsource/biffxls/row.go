package biffxls

type rowInfo struct {
	Index    uint16
	Fcell    uint16
	Lcell    uint16
	Height   uint16
	Notused  uint16
	Notused2 uint16
	Flags    uint32
}

// Row the data of one row
type Row struct {
	wb   *WorkBook
	info *rowInfo
	cols map[uint16]contentHandler
}

// Col Get the Nth Col from the Row, if has not, return nil.
// Suggest use Has function to test it.
func (r *Row) Col(i int) string {
	serial := uint16(i)
	if ch, ok := r.cols[serial]; ok {
		strs := ch.String(r.wb)
		return strs[0]
	} else {
		for _, v := range r.cols {
			if v.FirstCol() <= serial && v.LastCol() >= serial {
				strs := v.String(r.wb)
				return strs[serial-v.FirstCol()]
			}
		}
	}
	return ""
}

// LastCol Get the number of Last Col of the Row.
func (r *Row) LastCol() int {
	return int(r.info.Lcell)
}

// FirstCol Get the number of First Col of the Row.
func (r *Row) FirstCol() int {
	return int(r.info.Fcell)
}

// LastDefinedCol returns the highest column index that actually carries a cell
// record, or -1 for an empty row. FORK ADDITION: info.Lcell is the BIFF colMac
// field (one past the last defined column) and is 0 for rows synthesised from
// cell records without a matching ROW record, so it cannot be used to size a
// row.
func (r *Row) LastDefinedCol() int {
	last := -1
	for _, ch := range r.cols {
		if c := int(ch.LastCol()); c > last {
			last = c
		}
	}
	return last
}
