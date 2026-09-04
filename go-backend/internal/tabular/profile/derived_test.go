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
