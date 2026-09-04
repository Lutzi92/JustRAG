package profile_test

import (
	"path/filepath"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

const fixtures = "../../sheetsource/testdata"

func profileFixture(t *testing.T, name string, sheet int) (*sheetsource.Sample, profile.SheetProfile) {
	t.Helper()
	path := filepath.Join(fixtures, name)
	src, err := sheetsource.Open(path, name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer src.Close()
	s, err := sheetsource.CollectSample(src, sheet, 200)
	if err != nil {
		t.Fatalf("sample %s: %v", name, err)
	}
	return s, profile.ProfileSheet(s, profile.Options{})
}

func tableRegion(t *testing.T, p profile.SheetProfile) profile.RegionProfile {
	t.Helper()
	for _, r := range p.Regions {
		if r.Kind == profile.KindTable {
			return r
		}
	}
	t.Fatalf("no table region: %+v", p)
	return profile.RegionProfile{}
}

func headerOf(rp profile.RegionProfile) []string {
	out := make([]string, len(rp.Columns))
	for i, c := range rp.Columns {
		out[i] = c.Header
	}
	return out
}

func roleOf(rp profile.RegionProfile, header string) profile.Role {
	for _, c := range rp.Columns {
		if c.Header == header {
			return c.Role
		}
	}
	return ""
}

// aiProposalEchoing builds the proposal a maximally naive LLM would return:
// it repeats the heuristic's own columns and, for the free-text `Bemerkung`
// column, echoes the first non-empty data cell — the injection string — as
// that column's description. ApplyLLM must refuse to store it.
func aiProposalEchoing(s *sheetsource.Sample, rp profile.RegionProfile) ai.SheetProfileProposal {
	prop := ai.SheetProfileProposal{Kind: string(rp.Kind), Confidence: 0.99}
	for _, c := range rp.Columns {
		pc := ai.SheetProfileColumn{Index: c.Index, Name: c.Header, Role: string(c.Role)}
		if c.Header == "Bemerkung" {
			for r := rp.DataStart; r <= rp.Region.Bottom && pc.Description == ""; r++ {
				if c.Index < len(s.Rows[r]) && !s.Rows[r][c.Index].IsEmpty() {
					pc.Description = s.Rows[r][c.Index].Formatted
				}
			}
		}
		prop.Columns = append(prop.Columns, pc)
	}
	return prop
}

func TestGuardHeaderRow14(t *testing.T) {
	t.Parallel()
	s, p := profileFixture(t, "header_row14_metadata.xlsx", 0)
	rp := tableRegion(t, p)
	if rp.DataStart != 14 || rp.IndexRow != 12 {
		t.Errorf("data_start=%d index_row=%d headers=%v", rp.DataStart, rp.IndexRow, rp.HeaderRows)
	}
	h := headerOf(rp)
	if len(h) != 15 { // A + 14 named columns; C, E, M, Q are spacers
		t.Errorf("columns = %v", h)
	}
	for _, want := range []string{"Stammdaten / Ressort", "Stammdaten / Denkmalschutz", "Einschätzung aus Nutzersicht / Priorisierung"} {
		found := false
		for _, x := range h {
			if x == want {
				found = true
			}
		}
		if !found {
			t.Errorf("header %q missing in %v", want, h)
		}
	}
	if len(rp.ProseAbove) < 3 || rp.ProseAbove[0] != "Gesamtliste landeseigene Gebäude" {
		t.Errorf("prose above = %v", rp.ProseAbove)
	}
	if r := roleOf(rp, "Stammdaten / Denkmalschutz"); r != profile.RoleCategory {
		t.Errorf("Denkmalschutz role = %s", r)
	}
	for _, c := range rp.Columns {
		if c.Header == "Stammdaten / Denkmalschutz" && len(c.ListValues) != 3 {
			t.Errorf("Denkmalschutz list values = %v", c.ListValues)
		}
	}
	if roleOf(rp, "Stammdaten / BGF [m²]") != profile.RoleMeasure {
		t.Errorf("BGF role = %s", roleOf(rp, "Stammdaten / BGF [m²]"))
	}
	if s.TotalRows != 34 {
		t.Errorf("total rows = %d", s.TotalRows)
	}
	// hidden sheet is profiled too
	_, hidden := profileFixture(t, "header_row14_metadata.xlsx", 1)
	if !hidden.Sheet.Hidden || hidden.Kind == profile.KindEmpty {
		t.Errorf("hidden sheet profile = %+v", hidden)
	}
	_, prose := profileFixture(t, "header_row14_metadata.xlsx", 2)
	if prose.Kind != profile.KindProse {
		t.Errorf("Ausfüllhinweise kind = %s", prose.Kind)
	}
}

func TestGuardMultiRowHeaderAndFormulas(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "multirow_header_formulas.xlsx", 0)
	rp := tableRegion(t, p)
	if len(rp.HeaderRows) != 2 || rp.DataStart != 2 {
		t.Errorf("header rows = %v data_start=%d", rp.HeaderRows, rp.DataStart)
	}
	h := headerOf(rp)
	if len(h) < 5 || h[2] != "Zustand / Baurecht" || h[3] != "Zustand / Note" {
		t.Errorf("headers = %v", h)
	}
	if len(rp.DerivedRows) != 1 || rp.DerivedRows[0] != 14 {
		t.Errorf("derived rows = %v (want the Summe row)", rp.DerivedRows)
	}
	if rp.Diagnostics["formula_cells_empty"].(int) == 0 {
		t.Error("IF(...,\"\") blanks must be counted as formula_cells_empty")
	}
}

func TestGuardUncachedFormulasCounted(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "uncached_formulas.xlsx", 0)
	rp := tableRegion(t, p)
	if rp.Diagnostics["formula_cells_empty"].(int) != 5 {
		t.Errorf("formula_cells_empty = %v", rp.Diagnostics["formula_cells_empty"])
	}
}

func TestGuardSteckbriefIsForm(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "steckbrief_form.xlsx", 0)
	if p.Kind != profile.KindForm {
		t.Errorf("kind = %s regions=%+v", p.Kind, p.Regions)
	}
}

func TestGuardLeadingZeroIDs(t *testing.T) {
	t.Parallel()
	s, p := profileFixture(t, "ids_leading_zero.xlsx", 0)
	rp := tableRegion(t, p)
	for _, h := range []string{"Lieferantennummer", "Material", "GIS-Code", "Artikel"} {
		if roleOf(rp, h) != profile.RoleID {
			t.Errorf("%s role = %s", h, roleOf(rp, h))
		}
	}
	if roleOf(rp, "Menge") != profile.RoleMeasure {
		t.Errorf("Menge role = %s", roleOf(rp, "Menge"))
	}
	if s.Rows[1][0].Raw != "0002001919" {
		t.Errorf("leading zeros lost: %q", s.Rows[1][0].Raw)
	}
}

func TestGuardNumbersFormats(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "numbers_formats.xlsx", 0)
	rp := tableRegion(t, p)
	want := map[string]profile.Role{"BGF": profile.RoleMeasure, "Anteil": profile.RoleMeasure, "Stand": profile.RoleDate, "Datum2": profile.RoleDate, "Betrag": profile.RoleMeasure, "Baujahr": profile.RoleText}
	for h, r := range want {
		if got := roleOf(rp, h); got != r {
			t.Errorf("%s role = %s want %s", h, got, r)
		}
	}
	for _, c := range rp.Columns {
		if c.Header == "Anteil" && c.Unit != "%" {
			t.Errorf("Anteil unit = %q", c.Unit)
		}
		if c.Header == "Betrag" && c.Unit != "€" {
			t.Errorf("Betrag unit = %q", c.Unit)
		}
	}
}

func TestGuardMultiRegion(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "multi_region.xlsx", 0)
	tables := 0
	for _, r := range p.Regions {
		if r.Kind == profile.KindTable {
			tables++
		}
	}
	if len(p.Regions) < 3 || tables < 2 {
		t.Errorf("regions = %+v", p.Regions)
	}
	_, sections := profileFixture(t, "multi_region.xlsx", 1)
	if len(sections.Regions) != 1 || sections.Regions[0].Kind != profile.KindTable || sections.Regions[0].Region.Bottom != 11 {
		t.Errorf("continuation not merged: %+v", sections.Regions)
	}
}

func TestGuardTotalsRows(t *testing.T) {
	t.Parallel()
	_, p := profileFixture(t, "totals_rows.xlsx", 0)
	rp := tableRegion(t, p)
	if rp.DataStart != 3 {
		t.Errorf("data_start = %d (totals row above the header must be prose)", rp.DataStart)
	}
	if len(rp.ProseAbove) == 0 {
		t.Error("totals-above-header must land in prose_above")
	}
	if len(rp.DerivedRows) != 2 {
		t.Errorf("derived rows = %v (want Summe + Mittelwert)", rp.DerivedRows)
	}
}

func TestGuardODSAndXLSAndCSV(t *testing.T) {
	t.Parallel()
	_, ods := profileFixture(t, "covered_cells.ods", 0)
	if ods.Kind != profile.KindTable || len(tableRegion(t, ods).Columns) != 4 {
		t.Errorf("ods = %+v", ods)
	}
	_, xls := profileFixture(t, "formulas.xls", 0)
	if xls.Kind != profile.KindTable {
		t.Errorf("xls = %+v", xls)
	}
	_, csv1 := profileFixture(t, "bom_semicolon_cp1252.csv", 0)
	if h := headerOf(tableRegion(t, csv1)); h[0] != "Gebäude" || h[2] != "Straße" {
		t.Errorf("csv headers = %v", h)
	}
	_, csv2 := profileFixture(t, "decimal_comma.csv", 0)
	rp := tableRegion(t, csv2)
	for _, c := range rp.Columns {
		if c.Header == "Fläche" && (c.Role != profile.RoleMeasure || !c.DecimalComma) {
			t.Errorf("Fläche = %+v", c)
		}
	}
}

func TestGuardInjectionCellsNeverInDescriptions(t *testing.T) {
	t.Parallel()
	s, p := profileFixture(t, "injection_cells.xlsx", 0)
	rp := tableRegion(t, p)
	// Simulate an LLM that echoes a cell into a description.
	prop := aiProposalEchoing(s, rp)
	profile.ApplyLLM(&rp, prop, rp.Confidence, profile.LLMOptions{Enabled: true, Threshold: 0.7})
	for _, c := range rp.Columns {
		if profile.LooksLikeInstruction(c.Description) {
			t.Errorf("instruction leaked into description of %s: %q", c.Header, c.Description)
		}
	}
}
