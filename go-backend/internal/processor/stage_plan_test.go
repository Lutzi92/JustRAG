package processor

import "testing"

func TestBuildStagePlan(t *testing.T) {
	tests := []struct {
		name      string
		flags     stageFlags
		wantTotal int
		// stage -> expected 1-based index (0 means "not in plan")
		want map[string]int
	}{
		{
			name:      "minimal: parse+embed only",
			flags:     stageFlags{},
			wantTotal: 2,
			want:      map[string]int{stageParse: 1, stageEmbed: 2, stageKG: 0, stageRaptor: 0},
		},
		{
			name:      "all stages enabled",
			flags:     stageFlags{Tabular: true, Enrich: true, KG: true, HyPE: true, Raptor: true},
			wantTotal: 7,
			want: map[string]int{
				stageParse: 1, stageTabular: 2, stageEnrich: 3, stageEmbed: 4,
				stageKG: 5, stageHyPE: 6, stageRaptor: 7,
			},
		},
		{
			name:      "enrich + kg, no tabular/hype/raptor",
			flags:     stageFlags{Enrich: true, KG: true},
			wantTotal: 4,
			want:      map[string]int{stageParse: 1, stageEnrich: 2, stageEmbed: 3, stageKG: 4, stageRaptor: 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := buildStagePlan(tc.flags)
			if plan.total() != tc.wantTotal {
				t.Fatalf("total() = %d, want %d", plan.total(), tc.wantTotal)
			}
			for stage, wantIdx := range tc.want {
				if got := plan.indexOf(stage); got != wantIdx {
					t.Errorf("indexOf(%q) = %d, want %d", stage, got, wantIdx)
				}
			}
		})
	}
}
