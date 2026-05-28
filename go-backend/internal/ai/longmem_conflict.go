package ai

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// LongmemConflictAction is the verdict the conflict classifier
// returns for a candidate (new fact, existing nearby facts) pair.
type LongmemConflictAction string

const (
	// LongmemConflictCreateNew means the new fact is meaningfully
	// distinct from every nearby existing fact; insert normally.
	LongmemConflictCreateNew LongmemConflictAction = "create_new"
	// LongmemConflictSupersede means the new fact contradicts /
	// overrides one of the existing facts; insert the new row and
	// mark the older row's superseded_by = new.id. The caller pulls
	// the target ID from LongmemConflictVerdict.SupersedeIDs.
	LongmemConflictSupersede LongmemConflictAction = "supersede"
	// LongmemConflictSkipRedundant means the new fact restates an
	// existing fact without adding signal; drop without inserting.
	// The existing row's recency / access_count will be bumped by
	// the next RecallSemantic that surfaces it, so memory durability
	// is preserved despite the skip.
	LongmemConflictSkipRedundant LongmemConflictAction = "skip_redundant"
)

// LongmemConflictVerdict is the parsed LLM verdict. SupersedeIDs is
// populated only when Action == supersede; it lists the IDs of the
// existing memories the new fact should override (typically one,
// but the prompt allows the model to flag multiple stale entries
// that the same new fact replaces — e.g. "I now work on project B"
// supersedes both an old "works on A" and a stale "starting on A").
type LongmemConflictVerdict struct {
	Action       LongmemConflictAction `json:"action"`
	SupersedeIDs []int64               `json:"supersede_ids,omitempty"`
}

// LongmemCandidate is a compact projection of a longmem.Memory row
// passed to the conflict classifier. ID round-trips so the LLM can
// reference a specific existing row in its verdict's SupersedeIDs.
type LongmemCandidate struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// ClassifyLongmemConflict asks a fast-tier LLM to classify a
// candidate fact against its nearest existing memories (T1-3 Mem0
// pattern). Returns the verdict; on parse / LLM failure returns a
// CreateNew verdict + the error so the caller can choose to fall
// back to plain Insert (safer than dropping the fact silently).
//
// Empty candidates is a short-circuit: there's nothing to supersede
// or be redundant with, so the action is always CreateNew and no
// LLM call fires.
func ClassifyLongmemConflict(ctx context.Context, resolver *ConfigResolver, newFact LongmemCandidate, candidates []LongmemCandidate, kbID, lang, modelOverride string) (LongmemConflictVerdict, error) {
	if len(candidates) == 0 {
		return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, nil
	}

	// Marshal both the new fact and the candidate set so the LLM
	// sees structured input. The candidates carry IDs because the
	// verdict references them in SupersedeIDs.
	type promptInput struct {
		NewFact    LongmemCandidate   `json:"new_fact"`
		Candidates []LongmemCandidate `json:"existing_facts"`
	}
	payload, mErr := json.Marshal(promptInput{NewFact: newFact, Candidates: candidates})
	if mErr != nil {
		// JSON marshalling of a tagged struct can't fail in practice
		// (no Marshaler returning err, no unsupported types), but we
		// surface defensively so a future schema change can't silently
		// drop the request.
		return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, mErr
	}

	result, err := GenerateCompletionWithModel(ctx, resolver, string(payload), prompts.LongmemConflictPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, err
	}

	v, perr := parseLongmemConflictVerdict(result.Content)
	if perr != nil {
		return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, perr
	}
	return v, nil
}

// parseLongmemConflictVerdict extracts the verdict from raw LLM output.
// Tolerant of prose wrapping (mid-tier models occasionally prepend
// "Here is the verdict:" before the JSON) by retrying with the substring
// between the first '{' and last '}'. Unknown action strings normalise
// to CreateNew so a hallucinated action never causes a wrong supersede.
func parseLongmemConflictVerdict(text string) (LongmemConflictVerdict, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, nil
	}

	type raw struct {
		Action       string  `json:"action"`
		SupersedeIDs []int64 `json:"supersede_ids,omitempty"`
		// Defensive: some models emit the singular form.
		SupersedeID *int64 `json:"supersede_id,omitempty"`
	}
	var r raw
	b := []byte(text)
	if jerr := json.Unmarshal(b, &r); jerr != nil {
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start < 0 || end <= start {
			return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, jerr
		}
		if jerr2 := json.Unmarshal(b[start:end+1], &r); jerr2 != nil {
			return LongmemConflictVerdict{Action: LongmemConflictCreateNew}, jerr2
		}
	}

	v := LongmemConflictVerdict{SupersedeIDs: r.SupersedeIDs}
	if r.SupersedeID != nil && len(v.SupersedeIDs) == 0 {
		v.SupersedeIDs = []int64{*r.SupersedeID}
	}
	switch LongmemConflictAction(strings.ToLower(strings.TrimSpace(r.Action))) {
	case LongmemConflictSupersede:
		// Empty SupersedeIDs with action=supersede is incoherent — the
		// model said "override these older facts" without naming them.
		// Downgrade to CreateNew rather than silently doing nothing.
		if len(v.SupersedeIDs) == 0 {
			v.Action = LongmemConflictCreateNew
		} else {
			v.Action = LongmemConflictSupersede
		}
	case LongmemConflictSkipRedundant:
		v.Action = LongmemConflictSkipRedundant
		// SupersedeIDs is meaningless under skip_redundant — clear it
		// so callers don't accidentally act on stray IDs.
		v.SupersedeIDs = nil
	default:
		// Unknown / missing action → safest is to create the row so
		// no fact is lost. CreateNew clears SupersedeIDs to match.
		v.Action = LongmemConflictCreateNew
		v.SupersedeIDs = nil
	}
	return v, nil
}
