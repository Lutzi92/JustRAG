package vector

import "testing"

func TestEffectiveTopN_FallsBackToGlobalWhenAllSentinels(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{DefaultTopK: 15}
	for _, qt := range []string{QueryTypeLookup, QueryTypeEnumeration, QueryTypeComplexReasoning, QueryTypeUnknown, ""} {
		if got := EffectiveTopN(cfg, qt); got != 15 {
			t.Errorf("query_type=%q: want 15 (global), got %d", qt, got)
		}
	}
}

func TestEffectiveTopN_LookupOverride(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		DefaultTopK: 15,
		TopNLookup:  10,
	}
	if got := EffectiveTopN(cfg, QueryTypeLookup); got != 10 {
		t.Errorf("lookup override: want 10, got %d", got)
	}
	if got := EffectiveTopN(cfg, QueryTypeEnumeration); got != 15 {
		t.Errorf("enumeration with no override: want 15, got %d", got)
	}
}

func TestEffectiveTopN_AllOverridesSet(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		DefaultTopK:          15,
		TopNLookup:           10,
		TopNEnumeration:      30,
		TopNComplexReasoning: 25,
	}
	cases := map[string]int{
		QueryTypeLookup:           10,
		QueryTypeEnumeration:      30,
		QueryTypeComplexReasoning: 25,
		QueryTypeUnknown:          15,
	}
	for qt, want := range cases {
		if got := EffectiveTopN(cfg, qt); got != want {
			t.Errorf("query_type=%q: want %d, got %d", qt, want, got)
		}
	}
}

func TestEffectiveTopN_ZeroSentinelInherits(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		DefaultTopK:     15,
		TopNLookup:      0, // sentinel
		TopNEnumeration: 30,
	}
	if got := EffectiveTopN(cfg, QueryTypeLookup); got != 15 {
		t.Errorf("zero is sentinel for lookup: want 15, got %d", got)
	}
	if got := EffectiveTopN(cfg, QueryTypeEnumeration); got != 30 {
		t.Errorf("non-zero override applies: want 30, got %d", got)
	}
}
