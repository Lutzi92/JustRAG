//go:build manual

package profile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// Run: REALFILES="$HOME/Downloads/Dez. E und HRZ - KI-gestütztes Flächenmanagement" go test -tags manual ./internal/tabular/profile/ -run TestManualRealFiles -v
func TestManualRealFiles(t *testing.T) {
	dir := os.Getenv("REALFILES")
	if dir == "" {
		t.Skip("REALFILES not set")
	}
	expect := map[string]map[int]struct {
		kind      profile.SheetKind
		dataStart int
		minCols   int
	}{
		// Sheet 3 is the hidden `Dropdown` lookup sheet: fifteen small
		// one-column lists, so the first table region carries a single column.
		"250314_Gebäudeliste für Maßnahmenplan gem. HKlimaG §7(9)_JLU.xlsx": {1: {profile.KindTable, 14, 20}, 3: {profile.KindTable, 5, 1}},
		"250803_Immobilienportfolioanalyse JLU__Stand August 2026.xlsx":     {0: {profile.KindTable, 5, 15}},
		"Gebäudedaten der JLU aus CAFM.xlsx":                                {0: {profile.KindTable, 1, 23}},
		"Raumdaten der JLU aus CAFM.xlsx":                                   {0: {profile.KindTable, 1, 31}},
		"240611_JLU_Denkmalgeschützte_Gebäude_Wy.xlsx":                      {0: {profile.KindTable, 1, 10}},
	}
	for file, sheets := range expect {
		src, err := sheetsource.Open(filepath.Join(dir, file), file)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		for idx, want := range sheets {
			s, err := sheetsource.CollectSample(src, idx, 200)
			if err != nil {
				t.Errorf("%s[%d]: %v", file, idx, err)
				continue
			}
			p := profile.ProfileSheet(s, profile.Options{})
			var rp *profile.RegionProfile
			for i := range p.Regions {
				if p.Regions[i].Kind == profile.KindTable {
					rp = &p.Regions[i]
					break
				}
			}
			if p.Kind != want.kind || rp == nil || rp.DataStart != want.dataStart || len(rp.Columns) < want.minCols {
				t.Errorf("%s[%d] %s: kind=%s region=%+v", file, idx, s.Info.Name, p.Kind, rp)
				continue
			}
			t.Logf("%s[%d] %s: OK data_start=%d cols=%d derived=%v", file, idx, s.Info.Name, rp.DataStart, len(rp.Columns), rp.DerivedRows)
			for _, c := range rp.Columns {
				t.Logf("   %-40s %-9s unit=%q list=%d", c.Header, c.Role, c.Unit, len(c.ListValues))
			}
		}
		src.Close()
	}
}
