package biffxls

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

type boundsheet struct {
	Filepos uint32
	Type    byte
	Visible byte
	Name    byte
}

// WorkSheet in one WorkBook
type WorkSheet struct {
	bs   *boundsheet
	wb   *WorkBook
	Name string
	rows map[uint16]*Row
	//NOTICE: this is the max row number of the sheet, so it should be count -1
	MaxRow uint16
	parsed bool
	// FORK ADDITIONS: Merged holds the MERGEDCELLS ranges as
	// {firstRow, firstCol, lastRow, lastCol}; lastFormula points at the
	// FORMULA record whose cached string result a following STRING record
	// completes.
	Merged      [][4]int
	lastFormula *FormulaCol
	// parseErr holds the first error that ended record parsing for this
	// sheet. FORK FIX: upstream printed it to stdout, which a library must
	// never do; ParseErr lets the caller decide.
	parseErr error
}

// ParseErr reports the error that ended record parsing for this sheet, or nil
// when the sheet's records were consumed to the end-of-sheet marker. A
// non-nil value means the rows already decoded are a prefix of the sheet.
// FORK ADDITION.
func (w *WorkSheet) ParseErr() error { return w.parseErr }

func (w *WorkSheet) Row(i int) *Row {
	row := w.rows[uint16(i)]
	// FORK FIX: upstream dereferenced a missing row and panicked; its own
	// example loop guards with `if sheet.Row(i) == nil` which could never be
	// reached.
	if row == nil {
		return nil
	}
	row.wb = w.wb
	return row
}

// Hidden reports whether the sheet is hidden or very hidden.
//
// FORK ADDITION. NOTE: upstream named the two grbit bytes of the BOUNDSHEET
// record the wrong way round — the byte at offset 4 (field Type) is hsState
// (0 visible, 1 hidden, 2 very hidden) and the byte at offset 5 (field
// Visible) is the sheet type.
func (w *WorkSheet) Hidden() bool {
	if w.bs == nil {
		return false
	}
	return w.bs.Type&0x03 != 0
}

// CellAt returns the typed value at (row, col), 0-based. ok is false when the
// cell does not exist. FORK ADDITION.
//
// A single map lookup: addContent registers a span record (MULRK / MULBLANK /
// HYPERLINK) under every column it covers. It used to be keyed on FirstCol
// only, so every interior column of a span fell through to a full scan of the
// row's map — O(n) per lookup, i.e. O(n²) for a row read left to right, and
// nondeterministic when two spans overlap because Go randomises map iteration
// order.
func (w *WorkSheet) CellAt(row, col int) (CellValue, bool) {
	r := w.rows[uint16(row)]
	if r == nil {
		return CellValue{}, false
	}
	ch, ok := r.cols[uint16(col)]
	if !ok {
		return CellValue{}, false
	}
	return cellValueOf(ch, w.wb, col-int(ch.FirstCol())), true
}

func cellValueOf(ch contentHandler, wb *WorkBook, i int) CellValue {
	if v, ok := ch.(valuer); ok {
		return v.ValueAt(wb, i)
	}
	s := ch.String(wb)
	if i >= 0 && i < len(s) {
		return CellValue{Text: s[i]}
	}
	return CellValue{}
}

func (w *WorkSheet) parse(buf io.ReadSeeker) {
	w.rows = make(map[uint16]*Row)
	b := new(bof)
	var bof_pre *bof
	for {
		if err := binary.Read(buf, binary.LittleEndian, b); err == nil {
			bof_pre = w.parseBof(buf, b, bof_pre)
			if b.Id == 0xa {
				break
			}
		} else {
			// FORK FIX: upstream did fmt.Println(err) here — a library
			// writing to stdout. io.EOF is the ordinary end of a stream that
			// simply has no EOF record and is not worth reporting.
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				w.parseErr = err
			}
			break
		}
	}
	w.parsed = true
}

func (w *WorkSheet) parseBof(buf io.ReadSeeker, b *bof, pre *bof) *bof {
	var col interface{}
	// FORK ADDITION: a STRING record carries the cached string result of the
	// FORMULA record it directly follows (SHRFMLA / ARRAY / TABLE / CONTINUE
	// may sit in between). Any other record ends that pairing.
	switch b.Id {
	case 0x06, 0x207, 0x3c, 0x4bc, 0x221, 0x236:
	default:
		w.lastFormula = nil
	}
	switch b.Id {
	case 0x0E5: //MERGEDCELLS
		bts := make([]byte, b.Size)
		binary.Read(buf, binary.LittleEndian, &bts)
		br := bytes.NewReader(bts)
		var n uint16
		if binary.Read(br, binary.LittleEndian, &n) == nil {
			for i := uint16(0); i < n; i++ {
				var rng struct{ FirstRow, LastRow, FirstCol, LastCol uint16 }
				if binary.Read(br, binary.LittleEndian, &rng) != nil {
					break
				}
				w.Merged = append(w.Merged, [4]int{int(rng.FirstRow), int(rng.FirstCol), int(rng.LastRow), int(rng.LastCol)})
			}
		}
	case 0x207: //STRING (cached string result of the preceding FORMULA)
		bts := make([]byte, b.Size)
		binary.Read(buf, binary.LittleEndian, &bts)
		if w.lastFormula != nil {
			br := bytes.NewReader(bts)
			var count uint16
			if binary.Read(br, binary.LittleEndian, &count) == nil {
				s, _ := w.wb.get_string(br, count)
				w.lastFormula.Str = &s
			}
		}
	case 0x208: //ROW
		r := new(rowInfo)
		binary.Read(buf, binary.LittleEndian, r)
		w.addRow(r)
	case 0x0BD: //MULRK
		mc := new(MulrkCol)
		size := (b.Size - 6) / 6
		binary.Read(buf, binary.LittleEndian, &mc.Col)
		mc.Xfrks = make([]XfRk, size)
		for i := uint16(0); i < size; i++ {
			binary.Read(buf, binary.LittleEndian, &mc.Xfrks[i])
		}
		binary.Read(buf, binary.LittleEndian, &mc.LastColB)
		col = mc
	case 0x0BE: //MULBLANK
		mc := new(MulBlankCol)
		size := (b.Size - 6) / 2
		binary.Read(buf, binary.LittleEndian, &mc.Col)
		mc.Xfs = make([]uint16, size)
		for i := uint16(0); i < size; i++ {
			binary.Read(buf, binary.LittleEndian, &mc.Xfs[i])
		}
		binary.Read(buf, binary.LittleEndian, &mc.LastColB)
		col = mc
	case 0x203: //NUMBER
		col = new(NumberCol)
		binary.Read(buf, binary.LittleEndian, col)
	case 0x06: //FORMULA
		// FORK FIX (ledger T6): the fixed FORMULA header is 20 bytes. A
		// truncated record made `b.Size-20` underflow the uint16 into ~65k
		// and constructed a cell from garbage; skip the body instead.
		if b.Size < 20 {
			buf.Seek(int64(b.Size), 1)
			break
		}
		c := new(FormulaCol)
		binary.Read(buf, binary.LittleEndian, &c.Header)
		c.Bts = make([]byte, b.Size-20)
		binary.Read(buf, binary.LittleEndian, &c.Bts)
		w.lastFormula = c
		col = c
	case 0x27e: //RK
		col = new(RkCol)
		binary.Read(buf, binary.LittleEndian, col)
	case 0xFD: //LABELSST
		col = new(LabelsstCol)
		binary.Read(buf, binary.LittleEndian, col)
	case 0x204:
		c := new(labelCol)
		binary.Read(buf, binary.LittleEndian, &c.BlankCol)
		var count uint16
		binary.Read(buf, binary.LittleEndian, &count)
		c.Str, _ = w.wb.get_string(buf, count)
		col = c
	case 0x201: //BLANK
		col = new(BlankCol)
		binary.Read(buf, binary.LittleEndian, col)
	case 0x1b8: //HYPERLINK
		var hy HyperLink
		binary.Read(buf, binary.LittleEndian, &hy.CellRange)
		buf.Seek(20, 1)
		var flag uint32
		binary.Read(buf, binary.LittleEndian, &flag)
		var count uint32

		if flag&0x14 != 0 {
			binary.Read(buf, binary.LittleEndian, &count)
			hy.Description = b.utf16String(buf, count)
		}
		if flag&0x80 != 0 {
			binary.Read(buf, binary.LittleEndian, &count)
			hy.TargetFrame = b.utf16String(buf, count)
		}
		if flag&0x1 != 0 {
			var guid [2]uint64
			binary.Read(buf, binary.BigEndian, &guid)
			if guid[0] == 0xE0C9EA79F9BACE11 && guid[1] == 0x8C8200AA004BA90B { //URL
				hy.IsUrl = true
				binary.Read(buf, binary.LittleEndian, &count)
				hy.Url = b.utf16String(buf, count/2)
			} else if guid[0] == 0x303000000000000 && guid[1] == 0xC000000000000046 { //URL{
				var upCount uint16
				binary.Read(buf, binary.LittleEndian, &upCount)
				binary.Read(buf, binary.LittleEndian, &count)
				// FORK FIX: same unbounded, file-controlled make() as
				// utf16String had; skip rather than allocate.
				if count > maxUTF16Chars {
					buf.Seek(int64(count), 1)
				} else {
					bts := make([]byte, count)
					binary.Read(buf, binary.LittleEndian, &bts)
					hy.ShortedFilePath = string(bts)
				}
				buf.Seek(24, 1)
				binary.Read(buf, binary.LittleEndian, &count)
				if count > 0 {
					binary.Read(buf, binary.LittleEndian, &count)
					buf.Seek(2, 1)
					hy.ExtendedFilePath = b.utf16String(buf, count/2+1)
				}
			}
		}
		if flag&0x8 != 0 {
			binary.Read(buf, binary.LittleEndian, &count)
			// FORK FIX: was an inline copy of utf16String, sharing its
			// count == 0 panic and its unbounded make().
			hy.TextMark = b.utf16String(buf, count)
		}

		w.addRange(&hy.CellRange, &hy)
	case 0x809:
		buf.Seek(int64(b.Size), 1)
	case 0xa:
	default:
		// log.Printf("Unknow %X,%d\n", b.Id, b.Size)
		buf.Seek(int64(b.Size), 1)
	}
	if col != nil {
		w.add(col)
	}
	return b
}

func (w *WorkSheet) add(content interface{}) {
	if ch, ok := content.(contentHandler); ok {
		if col, ok := content.(Coler); ok {
			w.addCell(col, ch)
		}
	}

}

func (w *WorkSheet) addCell(col Coler, ch contentHandler) {
	w.addContent(col.Row(), ch)
}

// maxRangeCells bounds how many cells one Ranger record (HYPERLINK) may
// register. The range comes straight from the file; the cell records the
// range covers already carry the values, so refusing an absurd range loses
// nothing but the link text on those cells.
const maxRangeCells = 4096

func (w *WorkSheet) addRange(rang Ranger, ch contentHandler) {
	rows := int(rang.LastRow()) - int(rang.FirstRow()) + 1
	cols := int(ch.LastCol()) - int(ch.FirstCol()) + 1
	if rows <= 0 || cols <= 0 || rows*cols > maxRangeCells {
		return
	}
	for i := rang.FirstRow(); ; i++ {
		w.addContent(i, ch)
		if i == rang.LastRow() {
			break
		}
	}
}

// maxSpanCols bounds how many column keys one span record may claim.
// LastCol() comes from the file, so an unbounded loop here would be another
// out-of-memory vector; 16384 is the widest column count any spreadsheet
// format allows.
const maxSpanCols = 16384

func (w *WorkSheet) addContent(row_num uint16, ch contentHandler) {
	var row *Row
	var ok bool
	if row, ok = w.rows[row_num]; !ok {
		info := new(rowInfo)
		info.Index = row_num
		row = w.addRow(info)
	}
	// FORK FIX: register under every column the record covers, not just its
	// first — see CellAt. First registration wins, so an overlapping span
	// (and a HYPERLINK record, which arrives after the cell records it
	// covers) can no longer displace a value already stored for a column.
	first, last := ch.FirstCol(), ch.LastCol()
	if last < first {
		last = first
	}
	if int(last)-int(first) >= maxSpanCols {
		last = first + maxSpanCols - 1
	}
	for c := first; ; c++ {
		if _, exists := row.cols[c]; !exists {
			row.cols[c] = ch
		}
		if c == last { // compared here, not in the loop head: last may be 0xFFFF
			break
		}
	}
}

func (w *WorkSheet) addRow(info *rowInfo) (row *Row) {
	if info.Index > w.MaxRow {
		w.MaxRow = info.Index
	}
	var ok bool
	if row, ok = w.rows[info.Index]; ok {
		row.info = info
	} else {
		row = &Row{info: info, cols: make(map[uint16]contentHandler)}
		w.rows[info.Index] = row
	}
	return
}
