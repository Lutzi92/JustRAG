package siteconfig

import (
	"fmt"
	"strconv"
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
	"user_file_quota_bytes":      validateUserFileQuotaBytes,

	"chat_library_fulltext_max_tokens": validateLibraryFulltextMaxTokens,
}

// Mirrors chat.ChatLibraryFulltextMaxTokens' clamp; out-of-range values would
// otherwise silently read as the default.
func validateLibraryFulltextMaxTokens(v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("must be an integer number of tokens")
	}
	if n < 4000 || n > 200000 {
		return fmt.Errorf("must be between 4000 and 200000 tokens")
	}
	return nil
}

// userFileQuotaMaxBytes mirrors chat.UserFileQuotaMax (1 TiB); siteconfig
// cannot import chat. The reader treats anything outside [0, max] as 0 =
// unlimited, so without this a typo would silently disable the quota.
const userFileQuotaMaxBytes = 1 << 40

func validateUserFileQuotaBytes(v string) error {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return fmt.Errorf("must be an integer number of bytes")
	}
	if n < 0 || n > userFileQuotaMaxBytes {
		return fmt.Errorf("must be between 0 and %d bytes (0 = unlimited)", int64(userFileQuotaMaxBytes))
	}
	return nil
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
