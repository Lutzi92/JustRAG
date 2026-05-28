package vector

import "testing"

func TestStepBackOutcome(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		stepBack  bool
		queryType string
		want      string
	}{
		{"disabled regardless of type", false, QueryTypeComplexReasoning, "skipped_disabled"},
		{"disabled on lookup", false, QueryTypeLookup, "skipped_disabled"},
		{"disabled on empty", false, "", "skipped_disabled"},
		{"enabled + complex_reasoning fires", true, QueryTypeComplexReasoning, "fired"},
		{"enabled + lookup skipped", true, QueryTypeLookup, "skipped_route"},
		{"enabled + enumeration skipped", true, QueryTypeEnumeration, "skipped_route"},
		{"enabled + unknown skipped", true, QueryTypeUnknown, "skipped_route"},
		{"enabled + empty skipped", true, "", "skipped_route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stepBackOutcome(tc.stepBack, tc.queryType); got != tc.want {
				t.Errorf("stepBack=%v queryType=%q: want %q, got %q", tc.stepBack, tc.queryType, tc.want, got)
			}
		})
	}
}
