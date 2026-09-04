package sheetsource

import "testing"

func TestOpenDispatch(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"header_row14_metadata.xlsx", "formulas.xls", "covered_cells.ods", "decimal_comma.csv"} {
		src, err := Open("testdata/"+f, f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if len(src.Sheets()) == 0 {
			t.Errorf("%s: no sheets", f)
		}
		src.Close()
	}
	if _, err := Open("testdata/x.xlsm", "x.xlsm"); err != ErrUnsupported {
		t.Errorf("xlsm: %v", err)
	}
}
