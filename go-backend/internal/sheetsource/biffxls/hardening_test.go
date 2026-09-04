package biffxls

import (
	"bytes"
	"testing"
)

// TestUTF16StringGuards covers the two file-controlled-count hazards in
// utf16String: count == 0 made bts[:len(bts)-1] slice out of range, and a
// large count made an unbounded allocation off a single uint32 field.
func TestUTF16StringGuards(t *testing.T) {
	t.Parallel()
	b := new(bof)

	t.Run("zero count returns empty without panic", func(t *testing.T) {
		t.Parallel()
		r := bytes.NewReader([]byte{'A', 0, 0, 0})
		if got := b.utf16String(r, 0); got != "" {
			t.Errorf("utf16String(count=0) = %q, want %q", got, "")
		}
		// Nothing consumed: the record's own bytes stay for the caller.
		if pos, _ := r.Seek(0, 1); pos != 0 {
			t.Errorf("stream advanced to %d, want 0", pos)
		}
	})

	t.Run("normal count decodes and drops the terminator", func(t *testing.T) {
		t.Parallel()
		// "Hi" + NUL, little-endian UTF-16; count includes the terminator.
		r := bytes.NewReader([]byte{'H', 0, 'i', 0, 0, 0})
		if got := b.utf16String(r, 3); got != "Hi" {
			t.Errorf("utf16String = %q, want %q", got, "Hi")
		}
	})

	t.Run("huge count is skipped, not allocated", func(t *testing.T) {
		t.Parallel()
		const count = maxUTF16Chars + 1
		r := bytes.NewReader(make([]byte, 8))
		if got := b.utf16String(r, count); got != "" {
			t.Errorf("utf16String(count=%d) = %q, want %q", count, got, "")
		}
		// The refused bytes are still accounted for, so the next record
		// header is read from the offset the record claims (past EOF here,
		// which ends parsing — the point is that it is not silently
		// misaligned).
		pos, _ := r.Seek(0, 1)
		if pos != int64(count)*2 {
			t.Errorf("stream at %d, want %d", pos, int64(count)*2)
		}
	})
}

// TestParseBofFormulaTooShort covers ledger guard T6: a FORMULA record shorter
// than its 20-byte fixed header made `b.Size-20` underflow the uint16 into
// ~65k and built a cell out of whatever followed in the stream.
func TestParseBofFormulaTooShort(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	wb.addXf(&Xf8{Format: 0})

	t.Run("short record is skipped whole", func(t *testing.T) {
		t.Parallel()
		w := &WorkSheet{wb: wb, rows: map[uint16]*Row{}}
		body := make([]byte, 32) // more than the record claims
		r := bytes.NewReader(body)
		w.parseBof(r, &bof{Id: 0x06, Size: 10}, nil)
		if len(w.rows) != 0 {
			t.Errorf("rows = %+v, want none (no cell may be built from a truncated FORMULA)", w.rows)
		}
		if pos, _ := r.Seek(0, 1); pos != 10 {
			t.Errorf("stream at %d, want 10 (the record body must be skipped exactly)", pos)
		}
		if w.lastFormula != nil {
			t.Error("lastFormula set from a truncated FORMULA record")
		}
	})

	t.Run("full-size record still builds a cell", func(t *testing.T) {
		t.Parallel()
		w := &WorkSheet{wb: wb, rows: map[uint16]*Row{}}
		r := bytes.NewReader(make([]byte, 64))
		w.parseBof(r, &bof{Id: 0x06, Size: 22}, nil)
		if len(w.rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(w.rows))
		}
		if w.lastFormula == nil {
			t.Error("lastFormula not set for a well-formed FORMULA record")
		}
	})
}

// TestCellAtSpanIsDeterministic covers the MULRK span lookup: interior
// columns of a span used to miss the per-column map and fall through to a
// full map scan, which is O(n) per lookup and — with two overlapping spans —
// returned whichever handler Go's randomised map iteration reached first.
func TestCellAtSpanIsDeterministic(t *testing.T) {
	t.Parallel()
	wb := &WorkBook{Formats: map[uint16]*Format{}}
	wb.addXf(&Xf8{Format: 0}) // "General": neither date nor percent

	newMulrk := func(row, firstCol uint16, vals ...int32) *MulrkCol {
		mc := new(MulrkCol)
		mc.RowB, mc.FirstColB = row, firstCol
		for _, v := range vals {
			mc.Xfrks = append(mc.Xfrks, XfRk{Index: 0, Rk: rkInt(v, false)})
		}
		mc.LastColB = firstCol + uint16(len(vals)) - 1
		return mc
	}

	w := &WorkSheet{wb: wb, rows: map[uint16]*Row{}}
	w.addContent(3, newMulrk(3, 2, 10, 20, 30, 40)) // columns 2..5

	for i, want := range []float64{10, 20, 30, 40} {
		col := 2 + i
		cv, ok := w.CellAt(3, col)
		if !ok {
			t.Errorf("CellAt(3,%d): not found (interior span column missed the map)", col)
			continue
		}
		if !cv.IsNumber || cv.Number != want {
			t.Errorf("CellAt(3,%d) = %+v, want number %v", col, cv, want)
		}
	}
	if _, ok := w.CellAt(3, 1); ok {
		t.Error("CellAt(3,1): found a cell left of the span")
	}
	if _, ok := w.CellAt(3, 6); ok {
		t.Error("CellAt(3,6): found a cell right of the span")
	}

	// Overlapping spans: the first registration wins, every time. Before the
	// fix the fallback scan picked one at random per call.
	w2 := &WorkSheet{wb: wb, rows: map[uint16]*Row{}}
	w2.addContent(0, newMulrk(0, 0, 1, 2, 3, 4)) // columns 0..3
	w2.addContent(0, newMulrk(0, 2, 90, 91))     // columns 2..3, overlapping
	for range 50 {
		for col, want := range map[int]float64{0: 1, 1: 2, 2: 3, 3: 4} {
			cv, ok := w2.CellAt(0, col)
			if !ok || cv.Number != want {
				t.Fatalf("CellAt(0,%d) = %+v ok=%v, want %v from the first-registered span", col, cv, ok, want)
			}
		}
	}
}
