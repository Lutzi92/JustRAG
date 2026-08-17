package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/kbconfig"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// ApplyResult is the answer to both "what would applying this preset cost me?"
// (GET) and "what did it cost me?" (POST) — one shape, because the honest
// pre-apply warning and the post-apply receipt are the same fact measured
// before and after the write.
type ApplyResult struct {
	Preset string `json:"preset"`
	Label  string `json:"label"`
	// Overwrites names the KB's own settings the apply replaces with a
	// different value, sorted. It is what a confirmation dialog must count:
	// applying is destructive across the whole 21-key vocabulary, hand-set
	// keys included. Empty means the apply takes nothing away from the admin
	// — it only pins keys they had left alone.
	Overwrites []string `json:"overwrites"`

	// Effective names the bundle keys whose EFFECTIVE value changes, sorted —
	// what the KB will actually answer differently on. It is the number that
	// decides whether clicking is safe, and it is NOT bounded by Overwrites: a
	// KB with no overrides at all has zero Overwrites and can still have a
	// dozen Effective entries, because the apply pins every bundle key over
	// whatever the deployment global said. Reporting only Overwrites there
	// produced „Keine deiner eigenen Einstellungen wird überschrieben." while
	// (for instance) the globally-enabled supervisor was turned off for the KB.
	Effective []string `json:"effective"`

	// Pinned is how many settings the apply writes as per-KB rows — the size
	// of the bundle, i.e. the count that stops following the deployment
	// defaults afterwards. The workflow_preset marker is deliberately NOT
	// counted: it records provenance and changes no behaviour, so counting it
	// would inflate a number whose whole job is to say how much of the KB's
	// answering behaviour is now frozen.
	Pinned int `json:"pinned"`
}

// applyPayload builds the exact key/value set an apply writes: the preset's
// bundle, plus the workflow_preset provenance marker naming it.
//
// Two properties are load-bearing and are pinned by
// TestApplyPresetWritesExactlyTheBundlePlusTheMarker:
//
//   - Nothing outside the bundle appears. Task 1's argument for keeping
//     query_cache_enabled inside the shared vocabulary is that a key OUTSIDE
//     it survives an apply untouched forever; that promise is only worth
//     anything if apply cannot reach such a key at all.
//   - No value is ever nil. A nil would store SQL NULL through
//     kbconfig.Store.UpsertBatch, i.e. clear the key; an apply must only ever
//     set. The `store` interface below has no delete method for the same
//     reason — the guarantee is structural, not a convention.
func applyPayload(p Preset) map[string]*string {
	kv := make(map[string]*string, len(p.Bundle)+1)
	for k, v := range p.Bundle {
		val := v
		kv[k] = &val
	}
	id := p.ID
	kv["workflow_preset"] = &id
	return kv
}

// applyPlan is a fully validated, not-yet-written apply.
type applyPlan struct {
	kv     map[string]*string
	result ApplyResult
}

// badRequest marks an error as the caller's fault, so the HTTP layer can tell
// "this preset would create a conflict" (400, message shown to the admin —
// which is the whole point of not forcing a preset through) apart from "the
// database was unreachable" (500, message swallowed). Without the distinction
// the apply would have to choose one status for both, and a conflict reported
// as a 500 is indistinguishable from an outage.
type badRequest struct{ error }

// planApply resolves a preset id and runs every check the ordinary save path
// runs, writing nothing. It is the shared body of the preview (GET) and the
// apply (POST), which is what guarantees a preview can never advertise an
// apply the server would then reject.
//
// A returned error is the caller's fault — 400, message shown — exactly when
// errors.As finds a badRequest in it; anything else is infrastructure and is a
// 500. Either way NOTHING has been written when it returns an error.
//
// On an unreadable pre-change state this fails CLOSED, unlike
// kbconfig.PutSettings, which logs and saves anyway. The tradeoff is
// different: PutSettings would otherwise discard an admin's hand-composed
// batch over a transient read error, whereas an apply is a one-click,
// idempotent, retryable bulk write of 22 keys — refusing costs the admin
// nothing but a second click, while proceeding would write the whole
// vocabulary with the conflict check silently skipped. "A preset is not
// privileged" has to hold in the degraded case too.
func (h *Handler) planApply(ctx context.Context, kbID, presetID string) (*applyPlan, error) {
	p, ok := PresetByID(presetID)
	if !ok {
		// The rejected id is deliberately not echoed: the client knows what it
		// sent, and httputil.SanitizeError's path heuristic would replace the
		// whole message with a generic one the moment the id looked like a
		// path. The valid set is derived, never hand-listed.
		return nil, badRequest{fmt.Errorf("unbekannte Vorlage; gültig sind: %s", strings.Join(presetIDs(), ", "))}
	}

	kv := applyPayload(p)
	// Same per-key registry validation as PUT /api/kb/{id}/settings, over
	// sorted keys so a bundle with two bad values always reports the same one.
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := siteconfig.Validate(k, *kv[k]); err != nil {
			return nil, badRequest{err}
		}
	}

	overrides, err := h.store.ListKBOverrides(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("pipeline: list kb overrides: %w", err)
	}
	existing, err := h.effectiveState(ctx, overrides)
	if err != nil {
		return nil, err
	}

	// Task 1's guard proves no bundle conflicts with itself; this catches a
	// collision with state the KB already holds OUTSIDE the bundle.
	updates := make([]siteconfig.KeyValue, 0, len(kv))
	for _, k := range keys {
		updates = append(updates, siteconfig.KeyValue{Key: k, Value: kv[k]})
	}
	if err := siteconfig.ValidateConflicts(existing, updates); err != nil {
		return nil, badRequest{err}
	}

	return &applyPlan{
		kv: kv,
		result: ApplyResult{
			Preset: p.ID,
			Label:  p.Label,
			// Two counts, from the two maps already in hand: `overrides` is
			// what THIS ADMIN set, `existing` is what the KB currently
			// RESOLVES to. They answer different questions and the dialog
			// needs both — see ApplyResult's field comments.
			Overwrites: Overwrites(p.Bundle, overrides),
			Effective:  EffectiveChanges(p.Bundle, existing),
			Pinned:     len(p.Bundle),
		},
	}, nil
}

// presetIDs lists the curated preset ids in display order.
func presetIDs() []string {
	all := Presets()
	out := make([]string, 0, len(all))
	for _, p := range all {
		out = append(out, p.ID)
	}
	return out
}

// effectiveState is the pre-change view ValidateConflicts judges against: the
// KB's overrides laid over the deployment globals, for every registry key.
// The merge itself is kbconfig.EffectiveState — the same function the ordinary
// save path uses, so the two cannot reach different verdicts about the same KB.
func (h *Handler) effectiveState(ctx context.Context, overrides map[string]*string) (map[string]*string, error) {
	fields := siteconfig.All()
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	globals, err := h.global.GetSiteConfigValues(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read global site config: %w", err)
	}
	return kbconfig.EffectiveState(overrides, globals), nil
}
