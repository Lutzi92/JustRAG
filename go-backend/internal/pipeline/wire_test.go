package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

// keysOf returns the top-level JSON object keys of v.
func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func assertKeys(t *testing.T, what string, got map[string]bool, want []string) {
	t.Helper()
	for _, k := range want {
		if !got[k] {
			t.Errorf("%s: missing JSON key %q (got %v)", what, k, got)
		}
	}
	for k := range got {
		found := false
		for _, w := range want {
			if k == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: unexpected JSON key %q — the wire contract is camelCase; "+
				"an untagged embedded struct field is the usual cause", what, k)
		}
	}
}

// TestApplyResultWireFormat pins the preset apply/preview response shape, in
// both directions, the same way TestWireFormatIsCamelCase pins the projection.
//
// Without it the POST/GET body was only ever round-tripped through its own
// struct, so renaming `overwrites` would break the confirmation dialog Task 5
// builds on it while every test stayed green — and that field is the one an
// admin's "N deiner Einstellungen werden überschrieben" warning is counted
// from.
//
// Asserted on the live endpoint body rather than on a struct literal, so a
// handler that wrapped or renamed the payload on its way out would be caught
// too. The fixture sets crag_enabled against "Hohe Präzision" (which wants it
// on) precisely so Overwrites is non-empty: an empty slice still marshals the
// key, but a populated one also proves the field carries what it claims.
//
// `effective` and `pinned` are pinned here for the same reason: the dialog's
// leading sentence („N Einstellungen ändern dadurch ihr Verhalten.") is counted
// from `effective`, and the pinning sentence from `pinned`. Renaming either
// would silently reduce the dialog to the old, understating warning.
func TestApplyResultWireFormat(t *testing.T) {
	s := newRecordingStore(map[string]string{"crag_enabled": "false"})
	rec := doPreview(t, newApplyHandler(s, map[string]string{}), "preset=high_precision")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	assertKeys(t, "ApplyResult", keysOf(t, json.RawMessage(rec.Body.Bytes())),
		[]string{"preset", "label", "overwrites", "effective", "pinned"})

	// The POST answers in the same shape — pinned here too, so the two cannot
	// drift apart and leave Task 5 parsing one of them wrongly.
	applied := doApply(t, newApplyHandler(newRecordingStore(nil), map[string]string{}), `{"preset":"high_precision"}`)
	if applied.Code != 200 {
		t.Fatalf("apply status = %d, want 200: %s", applied.Code, applied.Body.String())
	}
	assertKeys(t, "ApplyResult(POST)", keysOf(t, json.RawMessage(applied.Body.Bytes())),
		[]string{"preset", "label", "overwrites", "effective", "pinned"})
}

// TestWireFormatIsCamelCase pins the endpoint's JSON key set. The frontend types
// against exactly these names; a casing change here silently breaks the canvas
// with no compile error on either side.
func TestWireFormatIsCamelCase(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":            "true",
		"chat_drift_enabled":      "true",
		"chat_supervisor_enabled": "true",
	}}
	g, err := Project(context.Background(), r, fakeReader{vals: map[string]string{}}, LaneComplex, AgentBinding{})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	assertKeys(t, "ProjectedGraph", keysOf(t, g),
		[]string{"lane", "nodes", "edges", "orchestrators", "estLlmCalls", "estLatencyMs", "fields",
			// Preset provenance (Phase 5 Task 4). presetBaseKnown is the wire
			// form of PresetBaseFor's third state: it says whether
			// `deviations` is a number with a preset behind it, so the canvas
			// cannot render "0 Abweichungen" against a base that no longer
			// exists.
			"presetBase", "presetBaseKnown", "deviations",
			// The KB's default agent/team binding (Phase 6 Task 3). Top-level,
			// not on the node, because Options and ID are values Project has no
			// argument for — see AgentBindingInfo.
			"agentBinding"})

	if len(g.Nodes) == 0 {
		t.Fatal("projection returned no nodes")
	}
	// Pick a node that has keys, a reason, a condition and values populated so no field is
	// omitted by omitempty and silently escapes the check. condition is operationally critical
	// (orchestrator_bypass carries the most surprising explanation) so it must not drift.
	var sample *ProjectedNode
	for i := range g.Nodes {
		if len(g.Nodes[i].Keys) > 0 && g.Nodes[i].Reason != "" && g.Nodes[i].Condition != "" {
			sample = &g.Nodes[i]
			break
		}
	}
	if sample == nil {
		t.Fatal("no node with Keys, Reason, and Condition all non-empty — adjust the fixture")
	}
	assertKeys(t, "ProjectedNode", keysOf(t, sample),
		[]string{"id", "label", "group", "help", "keys", "alwaysOn", "llmCalls",
			"latencyMs", "activation", "reason", "condition", "values", "origins", "editable"})

	if len(g.Edges) == 0 {
		t.Fatal("projection returned no edges")
	}
	var loopEdge *EdgeSpec
	for i := range g.Edges {
		if g.Edges[i].Loop {
			loopEdge = &g.Edges[i]
			break
		}
	}
	if loopEdge == nil {
		t.Fatal("no loop edge in topology")
	}
	assertKeys(t, "EdgeSpec(loop)", keysOf(t, loopEdge),
		[]string{"from", "to", "label", "loop", "maxIterations"})

	if len(g.Orchestrators) == 0 {
		t.Fatal("projection returned no orchestrator candidates")
	}
	// Verify the fallback (active) candidate has no condition (omitempty elides the key).
	fallback := &g.Orchestrators[len(g.Orchestrators)-1]
	assertKeys(t, "OrchestratorCandidate(fallback)", keysOf(t, fallback),
		[]string{"orchestrator", "activation"})

	// Find a conditional candidate (drift, when supervisor is the fallback) to verify
	// condition is present when non-empty.
	var condCandidate *OrchestratorCandidate
	for i := range g.Orchestrators {
		if g.Orchestrators[i].Condition != "" {
			condCandidate = &g.Orchestrators[i]
			break
		}
	}
	if condCandidate == nil {
		t.Fatal("no orchestrator candidate with a Condition — adjust the fixture")
	}
	assertKeys(t, "OrchestratorCandidate(conditional)", keysOf(t, condCandidate),
		[]string{"orchestrator", "activation", "condition"})
}

// TestAgentBindingWireFormat pins the binding payload, on the LIVE endpoint
// body rather than on a struct literal — the same reason TestApplyResultWireFormat
// does: only the endpoint proves the handler actually fills `agentBinding`
// after Project, and only the endpoint can produce a non-empty `options`.
//
// Every field here has exactly one consumer, Task 4's inspector, and each of
// them is load-bearing: `kind` selects which attach endpoint the write goes to
// (…/agents/{id} vs …/teams/{id}), `id` is its path segment, `name` is what the
// dropdown renders, and the top-level `id` is how the control knows which
// option is selected. A rename would leave the control silently unable to
// preselect or to save, with no compile error on either side.
// Read off the RAW body, never off a decoded struct: decoding and re-encoding
// go through the same struct tags, so a renamed tag would round-trip and the
// assertion would pass on a payload the frontend can no longer read.
func TestAgentBindingWireFormat(t *testing.T) {
	rec := doGet(t, newBindingHandler(boundTeam()), "/api/kb/kb-1/workflow")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	raw, ok := body["agentBinding"]
	if !ok {
		t.Fatalf("no `agentBinding` on the wire — got %v", body)
	}
	// `disabled` and `emptyTeam` are the two "bound but cannot run" fields.
	// Both are on the wire rather than derived from `options`, because they are
	// what the inspector renders („abgeschaltet" / „kein aktives Mitglied") and
	// what tells the admin there is a row to deal with at all. They stay two
	// keys because the remedies differ — flip a switch vs staff a team.
	assertKeys(t, "AgentBindingInfo", keysOf(t, raw),
		[]string{"kind", "id", "name", "disabled", "emptyTeam", "options"})

	var info map[string]json.RawMessage
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal agentBinding: %v", err)
	}
	var opts []json.RawMessage
	if err := json.Unmarshal(info["options"], &opts); err != nil {
		t.Fatalf("unmarshal options: %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("no options on the wire — adjust the fixture")
	}
	// isDefault must NOT appear: the bound option is named once, at the top
	// level (see BindingOption.IsDefault). `disabled` and `emptyTeam` DO,
	// because "can I pick this one" is per-option and has no other carrier.
	assertKeys(t, "BindingOption", keysOf(t, opts[0]),
		[]string{"kind", "id", "name", "disabled", "emptyTeam"})
}
