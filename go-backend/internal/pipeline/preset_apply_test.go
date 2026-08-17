package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// recordingStore is a stateful fake of the per-KB override store: UpsertBatch
// merges into the same map ListKBOverrides serves, so a test can apply a preset
// and then ask what the KB now looks like — which is the only way to prove
// "applying did not disturb an unrelated override".
//
// It deliberately has NO delete method. The production `store` interface must
// not have one either: an apply that cannot delete cannot clear a key outside
// the bundle by construction, not merely by convention.
type recordingStore struct {
	overrides map[string]*string
	listErr   error
	upsertErr error
	upserts   []map[string]*string
}

func newRecordingStore(kv map[string]string) *recordingStore {
	s := &recordingStore{overrides: map[string]*string{}}
	for k, v := range kv {
		val := v
		s.overrides[k] = &val
	}
	return s
}

func (s *recordingStore) ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make(map[string]*string, len(s.overrides))
	for k, v := range s.overrides {
		out[k] = v
	}
	return out, nil
}

func (s *recordingStore) UpsertBatch(ctx context.Context, kbID string, kv map[string]*string) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	cp := make(map[string]*string, len(kv))
	for k, v := range kv {
		cp[k] = v
	}
	s.upserts = append(s.upserts, cp)
	for k, v := range kv {
		s.overrides[k] = v
	}
	return nil
}

// value returns the stored value for key, or "" plus false when unset.
func (s *recordingStore) value(key string) (string, bool) {
	v, ok := s.overrides[key]
	if !ok || v == nil {
		return "", false
	}
	return *v, true
}

func newApplyHandler(s *recordingStore, globals map[string]string) *Handler {
	return NewHandler(s, fakeReader{vals: globals}, fakeBindings{})
}

// withPresets swaps the curated preset table for the duration of one test.
// Needed because the real five bundles are guarded to be internally valid and
// conflict-free (TestPresetBundlesHaveNoInternalConflicts,
// TestPresetBundleKeysAreConfigurablePerKB), so no real preset can exercise the
// rejection paths. Restored via t.Cleanup; these tests never run in parallel.
func withPresets(t *testing.T, ps ...Preset) {
	t.Helper()
	orig := presets
	presets = ps
	t.Cleanup(func() { presets = orig })
}

func doApply(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/workflow/preset", strings.NewReader(body))
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.ApplyPreset(rec, req)
	return rec
}

func doPreview(t *testing.T, h *Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/workflow/preset?"+query, nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.PreviewPreset(rec, req)
	return rec
}

func decodeApplyResult(t *testing.T, rec *httptest.ResponseRecorder) ApplyResult {
	t.Helper()
	var res ApplyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return res
}

// ---------------------------------------------------------------------------
// Requirement 1: apply writes ONLY the bundle's keys, and clears nothing.
// ---------------------------------------------------------------------------

func TestApplyPresetWritesExactlyTheBundlePlusTheMarker(t *testing.T) {
	p, ok := PresetByID(PresetFast)
	if !ok {
		t.Fatal("PresetByID(fast)")
	}
	s := newRecordingStore(nil)
	h := newApplyHandler(s, map[string]string{})

	rec := doApply(t, h, `{"preset":"fast"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(s.upserts) != 1 {
		t.Fatalf("UpsertBatch called %d times, want exactly 1 (the write must be a single statement)", len(s.upserts))
	}

	got := s.upserts[0]
	want := map[string]string{}
	for k, v := range p.Bundle {
		want[k] = v
	}
	want["workflow_preset"] = PresetFast

	if len(got) != len(want) {
		t.Fatalf("wrote %d keys, want %d: %v", len(got), len(want), sortedKeys(got))
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("key %q was not written", k)
			continue
		}
		if gv == nil {
			t.Errorf("key %q written as NULL — an apply must never clear a key", k)
			continue
		}
		if *gv != wv {
			t.Errorf("key %q = %q, want %q", k, *gv, wv)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("apply wrote key %q, which is outside the bundle", k)
		}
	}
}

// The carried requirement in full: a key the admin set by hand that no preset
// mentions must be exactly as it was after an apply. Task 1's whole argument
// for keeping query_cache_enabled in the shared vocabulary rests on this.
func TestApplyPresetLeavesUnrelatedOverridesUntouched(t *testing.T) {
	s := newRecordingStore(map[string]string{
		// Per-KB registry keys deliberately OUTSIDE the 21-key vocabulary.
		"mmr_lambda":              "0.9",
		"chat_agentic_max_hops":   "4",
		"bm25_simple_arm_enabled": "true",
	})
	h := newApplyHandler(s, map[string]string{})

	if rec := doApply(t, h, `{"preset":"high_precision"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	for key, want := range map[string]string{
		"mmr_lambda":              "0.9",
		"chat_agentic_max_hops":   "4",
		"bm25_simple_arm_enabled": "true",
	} {
		got, ok := s.value(key)
		if !ok {
			t.Errorf("override %q was cleared by the apply", key)
			continue
		}
		if got != want {
			t.Errorf("override %q = %q, want %q — the apply overwrote a key outside the bundle", key, got, want)
		}
	}
	if _, touched := s.upserts[0]["mmr_lambda"]; touched {
		t.Error("the write payload names mmr_lambda, which is outside the bundle")
	}
}

// ---------------------------------------------------------------------------
// Atomicity: a rejected apply writes nothing at all.
// ---------------------------------------------------------------------------

func TestApplyPresetValidationFailureWritesNothing(t *testing.T) {
	withPresets(t, Preset{
		ID:    PresetStandard, // a valid workflow_preset enum value…
		Label: "Kaputt",
		Bundle: map[string]string{
			"crag_enabled":      "true",
			"factcheck_in_chat": "vielleicht", // …but not a boolean
		},
	})

	s := newRecordingStore(map[string]string{"crag_enabled": "false"})
	h := newApplyHandler(s, map[string]string{})

	rec := doApply(t, h, `{"preset":"standard"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if len(s.upserts) != 0 {
		t.Fatalf("a rejected apply wrote %d batches, want 0", len(s.upserts))
	}
	// The valid half of the bundle must not have leaked through either.
	if v, _ := s.value("crag_enabled"); v != "false" {
		t.Fatalf("crag_enabled = %q, want the pre-apply value %q — the write was not all-or-nothing", v, "false")
	}
}

func TestApplyPresetConflictWithExistingStateIsRejected(t *testing.T) {
	// The bundle enables the legacy factuality verifier and — unlike the real
	// "Hohe Präzision" — says nothing about Self-RAG, so the KB's own
	// chat_self_rag_enabled survives into the merged state and collides.
	withPresets(t, Preset{
		ID:    PresetHighPrecision,
		Label: "Kollidiert",
		Bundle: map[string]string{
			"chat_factuality_verifier_enabled": "true",
			"citation_validation_enabled":      "true",
		},
	})

	s := newRecordingStore(map[string]string{"chat_self_rag_enabled": "true"})
	h := newApplyHandler(s, map[string]string{})

	rec := doApply(t, h, `{"preset":"high_precision"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chat_self_rag_enabled") {
		t.Errorf("body = %s, want the server's own conflict message naming the pair", rec.Body.String())
	}
	if len(s.upserts) != 0 {
		t.Fatalf("a conflicting apply wrote %d batches, want 0", len(s.upserts))
	}
}

// ValidateConflicts only judges pairs the batch itself touches. A KB that is
// already incoherent on a pair no preset mentions must still be able to adopt
// a preset — otherwise a legacy row would permanently lock the feature out.
func TestApplyPresetIgnoresPreexistingConflictOutsideTheBundle(t *testing.T) {
	s := newRecordingStore(map[string]string{
		"raptor_enabled":       "true",
		"parent_child_enabled": "true",
	})
	h := newApplyHandler(s, map[string]string{})

	if rec := doApply(t, h, `{"preset":"fast"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A store read failure must not fall through to the write: without the
// pre-change state there is no conflict check, and an apply is a discretionary
// bulk write of 22 keys — there is no admin-composed input to lose by refusing.
func TestApplyPresetFailsClosedWhenStateCannotBeRead(t *testing.T) {
	s := newRecordingStore(nil)
	s.listErr = errors.New("boom")
	h := newApplyHandler(s, map[string]string{})

	rec := doApply(t, h, `{"preset":"fast"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if len(s.upserts) != 0 {
		t.Fatalf("wrote %d batches despite an unreadable pre-change state, want 0", len(s.upserts))
	}
}

func TestApplyPresetRejectsUnknownAndMissingIDs(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown id", `{"preset":"nonsense"}`},
		{"empty id", `{"preset":""}`},
		{"no field", `{}`},
		{"broken json", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRecordingStore(nil)
			rec := doApply(t, newApplyHandler(s, map[string]string{}), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if len(s.upserts) != 0 {
				t.Fatalf("wrote %d batches for a rejected request, want 0", len(s.upserts))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Requirement 2: an honest "this overwrites N of your settings", before writing.
// ---------------------------------------------------------------------------

func TestOverwritesCountsOnlyKeysTheKBItselfSets(t *testing.T) {
	sp := func(s string) *string { return &s }
	bundle := map[string]string{
		"crag_enabled":                    "true",
		"factcheck_in_chat":               "true",
		"chat_sufficient_context_enabled": "false",
	}

	tests := []struct {
		name      string
		overrides map[string]*string
		want      []string
	}{
		{"nothing overridden — nothing is lost", map[string]*string{}, []string{}},
		{
			"an override that already equals the bundle is not a loss",
			map[string]*string{"crag_enabled": sp("1")},
			[]string{},
		},
		{
			"an override the bundle changes is a loss",
			map[string]*string{"crag_enabled": sp("false")},
			[]string{"crag_enabled"},
		},
		{
			"a cleared row sets nothing and so loses nothing",
			map[string]*string{"crag_enabled": nil},
			[]string{},
		},
		{
			"keys outside the bundle are never counted",
			map[string]*string{"mmr_lambda": sp("0.9")},
			[]string{},
		},
		{
			"reported sorted",
			map[string]*string{
				"factcheck_in_chat":               sp("false"),
				"chat_sufficient_context_enabled": sp("true"),
			},
			[]string{"chat_sufficient_context_enabled", "factcheck_in_chat"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Overwrites(bundle, tc.overrides)
			if got == nil {
				t.Fatal("Overwrites returned nil; it must return an empty slice")
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("Overwrites = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreviewPresetReportsOverwritesWithoutWriting(t *testing.T) {
	// crag_enabled is set by hand to the opposite of what "Hohe Präzision"
	// wants (true) — that one is a real loss. chat_drift_enabled matches the
	// bundle already, and mmr_lambda is outside it: neither counts.
	s := newRecordingStore(map[string]string{
		"crag_enabled":       "false",
		"chat_drift_enabled": "false",
		"mmr_lambda":         "0.9",
	})
	h := newApplyHandler(s, map[string]string{})

	rec := doPreview(t, h, "preset=high_precision")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	res := decodeApplyResult(t, rec)
	if res.Preset != PresetHighPrecision {
		t.Errorf("Preset = %q, want %q", res.Preset, PresetHighPrecision)
	}
	if res.Label == "" {
		t.Error("Label is empty — the dialog has nothing to name the preset with")
	}
	if strings.Join(res.Overwrites, ",") != "crag_enabled" {
		t.Errorf("Overwrites = %v, want [crag_enabled]", res.Overwrites)
	}
	if len(s.upserts) != 0 {
		t.Fatalf("the preview wrote %d batches, want 0", len(s.upserts))
	}
}

// The preview must run the same validation as the apply, so a dialog can never
// offer an apply the server would then reject.
func TestPreviewPresetSurfacesTheSameRejection(t *testing.T) {
	withPresets(t, Preset{
		ID:     PresetHighPrecision,
		Label:  "Kollidiert",
		Bundle: map[string]string{"chat_factuality_verifier_enabled": "true"},
	})
	s := newRecordingStore(map[string]string{"chat_self_rag_enabled": "true"})

	rec := doPreview(t, newApplyHandler(s, map[string]string{}), "preset=high_precision")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewPresetRejectsUnknownID(t *testing.T) {
	s := newRecordingStore(nil)
	if rec := doPreview(t, newApplyHandler(s, map[string]string{}), "preset=nonsense"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rec := doPreview(t, newApplyHandler(s, map[string]string{}), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing preset parameter", rec.Code)
	}
}

// The POST answers in the same shape, reporting what it actually overwrote —
// so a client that skipped the preview still learns what it cost.
func TestApplyPresetReportsWhatItOverwrote(t *testing.T) {
	s := newRecordingStore(map[string]string{"crag_enabled": "false"})
	rec := doApply(t, newApplyHandler(s, map[string]string{}), `{"preset":"high_precision"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	res := decodeApplyResult(t, rec)
	if strings.Join(res.Overwrites, ",") != "crag_enabled" {
		t.Errorf("Overwrites = %v, want [crag_enabled]", res.Overwrites)
	}
}

// The review finding this pins: a KB with NO overrides of its own gets
// Overwrites=[] — correctly, it loses nothing it set — while the apply still
// writes all 21 bundle keys and, on a deployment whose globals disagree with
// the bundle, changes how the KB answers. Overwrites alone therefore cannot be
// the dialog's only number. Effective is that second number.
//
// The fixture is the real deployment case named in the review: the supervisor
// orchestrator is on GLOBALLY and "Standard" turns it off for the KB.
func TestEffectiveCountsBehaviourChangesTheAdminNeverSetThemselves(t *testing.T) {
	s := newRecordingStore(nil) // the KB overrides nothing at all
	h := newApplyHandler(s, map[string]string{"chat_supervisor_enabled": "true"})

	rec := doPreview(t, h, "preset=standard")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	res := decodeApplyResult(t, rec)

	if len(res.Overwrites) != 0 {
		t.Fatalf("Overwrites = %v, want none — the KB sets nothing of its own", res.Overwrites)
	}
	if !contains(res.Effective, "chat_supervisor_enabled") {
		t.Errorf("Effective = %v, want it to name chat_supervisor_enabled: the global has "+
			"the supervisor ON and this preset turns it OFF for the KB, which is a "+
			"behaviour change the dialog must be able to state", res.Effective)
	}
	if res.Pinned != 21 {
		t.Errorf("Pinned = %d, want 21 (the bundle size, marker excluded)", res.Pinned)
	}
}

// The other direction: a key whose effective value ALREADY equals the bundle is
// not a behaviour change, however it got that way (global row, KB override, or
// nothing set anywhere and the code default agreeing).
func TestEffectiveIgnoresKeysThatAlreadyMatchTheBundle(t *testing.T) {
	// "Standard" states every vocabulary key at its code default except the two
	// kill switches, so a KB with nothing set and globals that say nothing has
	// nothing to change.
	s := newRecordingStore(nil)
	h := newApplyHandler(s, map[string]string{})

	rec := doPreview(t, h, "preset=standard")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	res := decodeApplyResult(t, rec)
	if len(res.Effective) != 0 {
		t.Errorf("Effective = %v, want none — every bundle key already resolves to what "+
			"the bundle states, so applying changes no behaviour", res.Effective)
	}
}

// Effective is measured against the EFFECTIVE state, so a KB override that
// re-states the global still counts as no change, and an override that
// contradicts the bundle counts once — not twice alongside Overwrites.
func TestEffectiveAndOverwritesAnswerDifferentQuestions(t *testing.T) {
	s := newRecordingStore(map[string]string{"crag_enabled": "false"})
	h := newApplyHandler(s, map[string]string{"chat_supervisor_enabled": "true"})

	rec := doPreview(t, h, "preset=high_precision")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	res := decodeApplyResult(t, rec)

	if strings.Join(res.Overwrites, ",") != "crag_enabled" {
		t.Errorf("Overwrites = %v, want [crag_enabled]", res.Overwrites)
	}
	if !contains(res.Effective, "crag_enabled") {
		t.Errorf("Effective = %v, want it to include the hand-set key it changes", res.Effective)
	}
	// "Hohe Präzision" wants the supervisor ON and the global already has it
	// ON, so it is NOT a behaviour change — unlike in the "Standard" case above.
	if contains(res.Effective, "chat_supervisor_enabled") {
		t.Errorf("Effective = %v, want chat_supervisor_enabled absent: the global already "+
			"has it on and this bundle wants it on", res.Effective)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Projection: presetBase + deviations.
// ---------------------------------------------------------------------------

func TestProjectionAfterApplyReportsBaseAndNoDeviations(t *testing.T) {
	s := newRecordingStore(nil)
	h := newApplyHandler(s, map[string]string{})

	if rec := doApply(t, h, `{"preset":"high_precision"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply: status = %d: %s", rec.Code, rec.Body.String())
	}

	g := projectionOf(t, h)
	if g.PresetBase != PresetHighPrecision {
		t.Errorf("PresetBase = %q, want %q", g.PresetBase, PresetHighPrecision)
	}
	if !g.PresetBaseKnown {
		t.Error("PresetBaseKnown = false for a live preset")
	}
	if len(g.Deviations) != 0 {
		t.Errorf("Deviations = %v right after an apply, want none", g.Deviations)
	}
}

func TestProjectionReportsExactlyTheChangedBundleKey(t *testing.T) {
	s := newRecordingStore(nil)
	h := newApplyHandler(s, map[string]string{})
	if rec := doApply(t, h, `{"preset":"high_precision"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply: status = %d: %s", rec.Code, rec.Body.String())
	}

	// The admin turns the correction loop back off by hand.
	off := "false"
	s.overrides["crag_enabled"] = &off

	g := projectionOf(t, h)
	if strings.Join(g.Deviations, ",") != "crag_enabled" {
		t.Errorf("Deviations = %v, want [crag_enabled]", g.Deviations)
	}
}

// Resetting a bundle key to inherit counts as a deviation too: the preset
// pinned it, and this KB no longer does.
func TestProjectionReportsAResetBundleKeyAsADeviation(t *testing.T) {
	s := newRecordingStore(nil)
	h := newApplyHandler(s, map[string]string{})
	if rec := doApply(t, h, `{"preset":"high_precision"}`); rec.Code != http.StatusOK {
		t.Fatalf("apply: status = %d: %s", rec.Code, rec.Body.String())
	}
	delete(s.overrides, "chat_supervisor_enabled")

	g := projectionOf(t, h)
	if strings.Join(g.Deviations, ",") != "chat_supervisor_enabled" {
		t.Errorf("Deviations = %v, want [chat_supervisor_enabled]", g.Deviations)
	}
}

func TestProjectionWithoutAPresetHasNoBaseAndNoDeviations(t *testing.T) {
	s := newRecordingStore(map[string]string{"crag_enabled": "true"})
	g := projectionOf(t, newApplyHandler(s, map[string]string{}))

	if g.PresetBase != "" {
		t.Errorf("PresetBase = %q, want empty", g.PresetBase)
	}
	if !g.PresetBaseKnown {
		t.Error("PresetBaseKnown = false for a KB that simply has no base — that is a normal state, not a stale one")
	}
	if len(g.Deviations) != 0 {
		t.Errorf("Deviations = %v, want none without a base to deviate from", g.Deviations)
	}
}

// A stored id no preset resolves any more must stay visible AND stay
// distinguishable from "no base": reporting 0 deviations against a preset that
// does not exist would be a number with nothing behind it.
func TestProjectionKeepsAStalePresetBaseDistinguishable(t *testing.T) {
	s := newRecordingStore(map[string]string{"workflow_preset": "retired_preset"})
	g := projectionOf(t, newApplyHandler(s, map[string]string{}))

	if g.PresetBase != "retired_preset" {
		t.Errorf("PresetBase = %q, want the stale value preserved", g.PresetBase)
	}
	if g.PresetBaseKnown {
		t.Error("PresetBaseKnown = true for an id no preset resolves")
	}
	if len(g.Deviations) != 0 {
		t.Errorf("Deviations = %v, want none — there is no bundle to compare against", g.Deviations)
	}
}

func projectionOf(t *testing.T, h *Handler) ProjectedGraph {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/workflow", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.GetWorkflow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetWorkflow: status = %d: %s", rec.Code, rec.Body.String())
	}
	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return g
}

func sortedKeys(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
