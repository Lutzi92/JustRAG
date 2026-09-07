package siteconfig

import (
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/chatpolicy"
)

// globalValidators maps a GLOBAL-ONLY site_config key to the validator its
// stored value must pass at save time (W6-R14). These keys carry JSON
// documents rather than scalars, and they have no kbConfigRegistry row (like
// kb_stale_days), so the registry's own FieldJSON validation — which is
// hard-wired to the workflow-preset shape — never sees them.
//
// The readers on the answer path fall back to the default on an unparseable
// value, so this hook is not the only line of defence; it is the one that
// gives the operator the error at the moment they typed it, instead of a
// silently ignored policy discovered days later.
var globalValidators = map[string]func(string) error{
	"chat_orchestrator_policy":   chatpolicy.ValidateOrchestratorPolicyJSON,
	"chat_answer_tools_by_route": chatpolicy.ValidateAnswerToolsByRouteJSON,
}

// ValidateGlobalValues rejects a site-config batch whose value for a validated
// global key does not parse (W6-R14). A nil or whitespace-only value clears
// the key back to its default and is always valid — an operator must be able
// to empty a field they filled in wrong.
//
// Unlike ValidateConflicts, this needs no view of the existing table: each
// value is self-contained, so the check is unconditional.
func ValidateGlobalValues(updates []KeyValue) error {
	for _, kv := range updates {
		fn, ok := globalValidators[kv.Key]
		if !ok || kv.Value == nil || strings.TrimSpace(*kv.Value) == "" {
			continue
		}
		if err := fn(*kv.Value); err != nil {
			return fmt.Errorf("%s: %w", kv.Key, err)
		}
	}
	return nil
}
