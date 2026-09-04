package tabular

import (
	"testing"

	"github.com/justrag/go-backend/internal/tabular/profile"
)

// TestAssembleSpecsDedupesShadowNameAgainstPrimary covers Ruling R8:
// FinalSpec synthesizes a shadow column's default "<name>_num" name without
// seeing sibling columns, so it can collide with an unrelated primary column
// whose own header happens to sanitize to exactly that string.
// assembleSpecs must dedupe the SHADOW away, never rename the primary, keep
// ShadowOf pointing at the primary's (unchanged) name, and carry the final
// deduped shadow name into the primary's ColumnStat.ShadowColumn.
func TestAssembleSpecsDedupesShadowNameAgainstPrimary(t *testing.T) {
	t.Parallel()
	cols := []profile.ColumnProfile{
		{Index: 0, Header: "Fläche", Role: profile.RoleMeasure},  // -> primary "flaeche"; mixed numeric/text below triggers a "flaeche_num" shadow
		{Index: 1, Header: "Fläche Num", Role: profile.RoleText}, // -> primary "flaeche_num", colliding with the shadow's default name
	}
	accs := NewAccumulators(cols, StatsOptions{})
	// col0: mixed numeric/non-numeric (2 of 3 numeric) -> RoleMeasure + 0<Numeric<NonEmpty triggers a shadow (see FinalSpec, "mixed" case).
	accs[0].Add(num("2143.28"))
	accs[0].Add(txt("Nachtrag 2019"))
	accs[0].Add(num("1500"))
	// col1: plain text, unrelated to col0.
	accs[1].Add(txt("hello"))
	accs[1].Add(txt("world"))

	specs, stats := assembleSpecs(accs, 3)

	if len(specs) != 3 {
		t.Fatalf("specs = %+v, want 3 entries (primary0, shadow0, primary1)", specs)
	}
	primary0, shadow0, primary1 := specs[0], specs[1], specs[2]

	if primary0.Name != "flaeche" || primary0.Type != TypeText {
		t.Errorf("primary0 = %+v, want Name=flaeche Type=text", primary0)
	}
	if primary1.Name != "flaeche_num" {
		t.Errorf("primary1.Name = %q, want unchanged %q (primaries win first claim on a name)", primary1.Name, "flaeche_num")
	}
	if shadow0.Name != "flaeche_num_2" {
		t.Errorf("shadow0.Name = %q, want deduped %q", shadow0.Name, "flaeche_num_2")
	}
	if shadow0.ShadowOf != "flaeche" {
		t.Errorf("shadow0.ShadowOf = %q, want %q (must still point at the primary)", shadow0.ShadowOf, "flaeche")
	}
	if shadow0.Type != TypeNumeric {
		t.Errorf("shadow0.Type = %v, want numeric", shadow0.Type)
	}

	// stats stays 1:1 with specs: primary0, shadow0, primary1.
	if len(stats) != 3 {
		t.Fatalf("stats = %+v, want 3 entries (1:1 with specs)", stats)
	}
	if stats[0].Name != "flaeche" || stats[0].ShadowColumn != "flaeche_num_2" {
		t.Errorf("stats[0] = %+v, want Name=flaeche ShadowColumn=flaeche_num_2", stats[0])
	}
	if stats[1].Name != "flaeche_num_2" {
		t.Errorf("stats[1] = %+v, want the shadow's own stat, Name=flaeche_num_2", stats[1])
	}
	if stats[2].Name != "flaeche_num" || stats[2].ShadowColumn != "" {
		t.Errorf("stats[2] = %+v, want Name=flaeche_num ShadowColumn=\"\"", stats[2])
	}
}
