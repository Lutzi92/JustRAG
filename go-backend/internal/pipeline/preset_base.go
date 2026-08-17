package pipeline

import (
	"context"
	"fmt"
	"strings"
)

// PresetBaseFor is the sole reader of the workflow_preset registry key
// (internal/siteconfig/registry.go): a provenance marker recording which
// curated preset a KB's configuration was last applied from, not a
// behaviour flag anything branches on. Read via GetSiteConfigValue with the
// key as a string literal — not a batch GetSiteConfigValues call — because
// that literal-key call is what TestEveryRegistryKeyIsRead's AST walk
// recognises (route 5, internal/siteconfig/registry_consistency_test.go); a
// batch read would leave the guard reporting workflow_preset as unread, the
// exact phantom-control failure that guard exists to catch.
//
// r may be either the deployment-global reader or a per-KB
// siteconfig.KBOverlayReader — both satisfy globalReader (the interface
// already used for h.global in handler.go), and passing the overlay is how
// a caller gets the KB's own override rather than the global default:
// KBOverlayReader.GetSiteConfigValue resolves the per-KB row first and only
// falls through to the base reader when the KB never set the key.
//
// Return contract distinguishes three states a caller cannot conflate:
//
//   - id == "" and found == true: the key is unset, or was explicitly
//     cleared to "" (the registry's own "freeform / no base" enum value,
//     see registry.go). Both collapse to the same fact — this KB is not
//     based on a preset — and that is a normal state, not an error.
//   - id != "" and found == true: the stored id names a preset
//     PresetByID can still resolve today. The caller (Task 4/5) may look up
//     its Label/Bundle and compute Deviations against it.
//   - id != "" and found == false: the stored id does not match any
//     current preset — it was applied from a preset that has since been
//     renamed or removed, or the row was hand-edited. This must NOT be
//     shown identically to "no base": the KB genuinely was set up from a
//     preset once, and silently treating that the same as freeform would
//     misreport its history. PresetBaseFor stops at making the two states
//     tell-apart-able; how to word the difference is a display decision for
//     whichever task renders it.
func PresetBaseFor(ctx context.Context, r globalReader) (id string, found bool, err error) {
	v, err := r.GetSiteConfigValue(ctx, "workflow_preset")
	if err != nil {
		return "", false, fmt.Errorf("pipeline: read workflow_preset: %w", err)
	}
	if v == nil {
		return "", true, nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return "", true, nil
	}
	if _, known := PresetByID(trimmed); !known {
		return trimmed, false, nil
	}
	return trimmed, true, nil
}
