package chat

import (
	"context"
	"testing"
)

// The default flipped to map_reduce in Wave 5 (W5-R1: 34/36 pooled decisive
// pairs, Wilson low 0.819, coverage +5.0 pp on the 24-question global-synthesis
// set). Unset — the only state an operator who never touched the key is in —
// must therefore read map_reduce.
func TestChatLongContextMode_UnsetDefaultsToMapReduce(t *testing.T) {
	for name, r := range map[string]SiteConfigReader{
		"missing key": &fakeSiteConfigReader{values: map[string]*string{}},
		"empty value": &fakeSiteConfigReader{values: map[string]*string{
			"chat_longcontext_mode": strPtr(""),
		}},
		"whitespace only": &fakeSiteConfigReader{values: map[string]*string{
			"chat_longcontext_mode": strPtr("   "),
		}},
	} {
		if got := ChatLongContextMode(context.Background(), r); got != LongContextModeMapReduce {
			t.Errorf("%s: got %q, want %q", name, got, LongContextModeMapReduce)
		}
	}
}

// A nil reader is "no site config at all", not "an operator chose flat".
func TestChatLongContextMode_NilReaderDefaultsToMapReduce(t *testing.T) {
	if got := ChatLongContextMode(context.Background(), nil); got != LongContextModeMapReduce {
		t.Errorf("nil reader: got %q, want %q", got, LongContextModeMapReduce)
	}
}

// An unrecognised value must NOT inherit the new default: a typo has to
// normalise to the SAFE mode (flat is the cheap, byte-identical-to-pre-Wave-3
// consumer), never to the one that fans out a fast-tier call per chunk group.
func TestChatLongContextMode_UnrecognisedNormalisesToFlat(t *testing.T) {
	for _, raw := range []string{"map-reduce", "mapreduce", "MAP REDUCE", "reduce", "flat!", "true", "0"} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_longcontext_mode": strPtr(raw),
		}}
		if got := ChatLongContextMode(context.Background(), r); got != LongContextModeFlat {
			t.Errorf("value %q: got %q, want %q", raw, got, LongContextModeFlat)
		}
	}
}

func TestChatLongContextMode_ExplicitValuesAreHonoured(t *testing.T) {
	for raw, want := range map[string]string{
		"flat":         LongContextModeFlat,
		"  FLAT  ":     LongContextModeFlat,
		"map_reduce":   LongContextModeMapReduce,
		" Map_Reduce ": LongContextModeMapReduce,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"chat_longcontext_mode": strPtr(raw),
		}}
		if got := ChatLongContextMode(context.Background(), r); got != want {
			t.Errorf("value %q: got %q, want %q", raw, got, want)
		}
	}
}
