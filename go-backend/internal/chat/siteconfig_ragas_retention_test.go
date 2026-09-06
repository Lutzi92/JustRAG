package chat

import (
	"context"
	"testing"
)

func TestRagasSamplesRetentionDays_Default(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := RagasSamplesRetentionDays(context.Background(), r); got != 90 {
		t.Errorf("missing key: got %d, want 90", got)
	}
}

func TestRagasSamplesRetentionDays_ParsesValid(t *testing.T) {
	for raw, want := range map[string]int{
		"1":    1,
		"7":    7,
		"365":  365,
		"3650": 3650,
	} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_samples_retention_days": strPtr(raw),
		}}
		if got := RagasSamplesRetentionDays(context.Background(), r); got != want {
			t.Errorf("raw=%q: got %d, want %d", raw, got, want)
		}
	}
}

// Out-of-range falls back to the default rather than clamping to the nearest
// bound: a "0" typed into the retention field would otherwise mean "delete
// every sample on tonight's pass", i.e. a data-loss knob rather than a
// retention one. parseInt's out-of-range → default behaviour is what makes
// that safe, so this test guards it explicitly for this key.
func TestRagasSamplesRetentionDays_RejectsOutOfRange(t *testing.T) {
	for _, raw := range []string{"0", "-1", "3651", "abc", ""} {
		r := &fakeSiteConfigReader{values: map[string]*string{
			"ragas_samples_retention_days": strPtr(raw),
		}}
		if got := RagasSamplesRetentionDays(context.Background(), r); got != 90 {
			t.Errorf("invalid value %q: got %d, want default 90", raw, got)
		}
	}
}

func TestRagasSamplesRetentionDays_NilReader(t *testing.T) {
	if got := RagasSamplesRetentionDays(context.Background(), nil); got != 90 {
		t.Errorf("nil reader: got %d, want 90", got)
	}
}
