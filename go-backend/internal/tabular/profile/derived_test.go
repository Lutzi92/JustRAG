package profile

import (
	"testing"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func TestIsDerivedRow(t *testing.T) {
	t.Parallel()
	label := col("Summe", "", "")
	label[1] = sheetsource.Cell{Kind: sheetsource.KindNumber, Raw: "100", IsFormula: true, Formula: "SUM(B4:B13)"}
	if !IsDerivedRow(label, []int{0, 1, 2}, 10) {
		t.Error("Summe label row must be derived")
	}
	f := []sheetsource.Cell{{Kind: sheetsource.KindText, Raw: "Gebäude 7"}, {Kind: sheetsource.KindNumber, Raw: "5", IsFormula: true, Formula: "IF(B7>=5,B7,\"\")"}}
	if IsDerivedRow(f, []int{0, 1}, 10) {
		t.Error("per-row IF formula is not derived")
	}
	agg := []sheetsource.Cell{{Kind: sheetsource.KindEmpty}, {Kind: sheetsource.KindNumber, Raw: "42", IsFormula: true, Formula: "SUBTOTAL(9,C4:C13)"}}
	if !IsDerivedRow(agg, []int{0, 1}, 10) {
		t.Error("SUBTOTAL over the column must be derived")
	}
	if formulaSpansRows("SUM(D3:D14)+G2") != 12 || formulaSpansRows("A1*2") != 0 {
		t.Error("formulaSpansRows")
	}
}

// TestIsDerivedRowLabelDoesNotShortCircuit pins that the aggregate-formula
// rule still runs when the row's first kept cell is text. Rule (a) used to
// `return` its regex result, so any row starting with an unrecognised label
// never reached rule (b) at all.
func TestIsDerivedRowLabelDoesNotShortCircuit(t *testing.T) {
	t.Parallel()
	const regionRows = 400

	sum := []sheetsource.Cell{
		{Kind: sheetsource.KindText, Raw: "Jahressumme"}, // not in totalsLabelRe
		{Kind: sheetsource.KindNumber, Raw: "1234", IsFormula: true, Formula: "SUM(C4:C400)"},
	}
	if !IsDerivedRow(sum, []int{0, 1}, regionRows) {
		t.Error(`"Jahressumme | =SUM(C4:C400)" must be derived: the label misses the regex, but the aggregate formula spans the region`)
	}

	note := []sheetsource.Cell{
		{Kind: sheetsource.KindText, Raw: "Anmerkung"},
		{Kind: sheetsource.KindNumber, Raw: "5"},
	}
	if IsDerivedRow(note, []int{0, 1}, regionRows) {
		t.Error(`"Anmerkung | 5" must not be derived: no totals label and no aggregate formula`)
	}
}

func TestAtoiCapsDigits(t *testing.T) {
	t.Parallel()
	if got := atoi("123456789"); got != 123456789 {
		t.Errorf("atoi(9 digits) = %d", got)
	}
	// 10+ digits are not row numbers; accumulating them overflows into
	// garbage, so they must read as 0 (no span, no derived-row claim).
	if got := atoi("12345678901234567890"); got != 0 {
		t.Errorf("atoi(20 digits) = %d, want 0", got)
	}
	// The absurd bound reads as 0, so the span collapses to abs(1-0)+1 = 2
	// instead of the overflowed (and possibly negative) value it used to be.
	if got := formulaSpansRows("SUM(A1:A12345678901234567890)"); got != 2 {
		t.Errorf("formulaSpansRows with an absurd row number = %d, want 2", got)
	}
}
