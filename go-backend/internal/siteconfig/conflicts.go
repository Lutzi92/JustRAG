package siteconfig

import (
	"fmt"
	"strings"
)

// conflictPair documents two boolean site_config flags that must not both be
// true. These pairs were previously enforced only by docs
// (docs/feature-recipes.md) and runtime skip logic, so an admin could save an
// incoherent combination silently; ValidateConflicts rejects such saves.
type conflictPair struct {
	a, b   string
	reason string
}

var conflictPairs = []conflictPair{
	{
		a: "raptor_enabled", b: "parent_child_enabled",
		reason: "RAPTOR hierarchical indexing and parent-child chunking are mutually exclusive ingestion strategies",
	},
	{
		a: "chat_self_rag_enabled", b: "chat_factuality_verifier_enabled",
		reason: "chat_self_rag_enabled REPLACES the legacy factuality verifier; running both would duplicate cost",
	},
}

// boolTrue mirrors the loose boolean parsing used by the chat/vector config
// readers: "true" and "1" (case-insensitive) are on, everything else
// (including nil / cleared) is off.
func boolTrue(v *string) bool {
	if v == nil {
		return false
	}
	s := strings.TrimSpace(*v)
	return strings.EqualFold(s, "true") || s == "1"
}

// ValidateConflicts rejects a batch update that would leave both halves of a
// documented mutually-exclusive flag pair enabled. existing is the current
// site_configs table content; updates is the incoming batch (a nil Value
// clears the key).
//
// A conflict is only reported when the batch itself touches at least one key
// of the offending pair — a deployment whose stored state is already
// incoherent (e.g. from before this validator existed) can still save
// unrelated keys, and fixing either flag of the pair is always possible.
func ValidateConflicts(existing map[string]*string, updates []KeyValue) error {
	merged := make(map[string]*string, len(existing)+len(updates))
	for k, v := range existing {
		merged[k] = v
	}
	touched := make(map[string]bool, len(updates))
	for _, kv := range updates {
		merged[kv.Key] = kv.Value
		touched[kv.Key] = true
	}

	for _, p := range conflictPairs {
		if !touched[p.a] && !touched[p.b] {
			continue
		}
		if boolTrue(merged[p.a]) && boolTrue(merged[p.b]) {
			return fmt.Errorf("%s and %s cannot both be enabled: %s", p.a, p.b, p.reason)
		}
	}
	return nil
}
