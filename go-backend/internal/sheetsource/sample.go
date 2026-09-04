package sheetsource

import "fmt"

type Sample struct {
	Info      SheetInfo
	Rows      [][]Cell // first N rows, dense, every row padded to Width
	Width     int
	Extras    SheetExtras // complete: CollectSample reads the whole sheet
	TotalRows int         // rows in the sheet (Extras.RowCount)
}

// CollectSample reads the entire sheet (so extras are complete) but keeps
// only the first n rows. Rows are padded with KindEmpty cells to Width.
func CollectSample(src Source, sheetIdx, n int) (*Sample, error) {
	infos := src.Sheets()
	if sheetIdx < 0 || sheetIdx >= len(infos) {
		return nil, fmt.Errorf("sheetsource: sheet %d out of range", sheetIdx)
	}
	s := &Sample{Info: infos[sheetIdx]}
	extras, err := src.ReadSheet(sheetIdx, func(rowIdx int, cells []Cell) error {
		if rowIdx < n {
			for len(s.Rows) < rowIdx { // keep indices aligned if a backend skips gap rows
				s.Rows = append(s.Rows, nil)
			}
			s.Rows = append(s.Rows, cells)
			if len(cells) > s.Width {
				s.Width = len(cells)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Extras = extras
	s.TotalRows = extras.RowCount
	for i := range s.Rows {
		for len(s.Rows[i]) < s.Width {
			s.Rows[i] = append(s.Rows[i], Cell{})
		}
	}
	return s, nil
}
