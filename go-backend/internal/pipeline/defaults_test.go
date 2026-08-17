package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The defaultOn map in project.go hand-duplicates defaults that actually live
// at the read call sites in internal/chat. Two of the projection's activation
// keys are default-ON and both were found by hand; a third that flips later
// would silently invert a node's rendered state with no test failing. The
// activation key IS what the diagram says about a node, so this guard is
// bidirectional: it checks the map against the code and the code against the
// map.
//
// Two read shapes carry a literal default and cover every activation key
// except the two read through internal/vector's siteConfigParsers registry
// (query_cache_enabled, recency_boost_enabled), whose default is the config
// struct's zero value and has no literal to compare against:
//
//  1. readBool(ctx, reader, "key", <true|false>)     → Args[3]
//  2. parseBool(values["key"], <true|false>)         → Args[1]
//
// Shape 2 is the batch path (loadCRAGConfig et al.), which is how crag_enabled,
// adaptive_routing_enabled and step_back_enabled get their defaults.

// boolDefaultSites maps a key to every literal default the walk saw for it.
type boolDefaultSites map[string]map[bool][]string

func collectBoolDefaults(t *testing.T) boolDefaultSites {
	t.Helper()
	found := boolDefaultSites{}

	record := func(key string, def bool, where string) {
		if found[key] == nil {
			found[key] = map[bool][]string{}
		}
		found[key][def] = append(found[key][def], where)
	}

	for _, pkgDir := range scannedPackages {
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			t.Fatalf("read dir %s: %v", pkgDir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(pkgDir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				where := fset.Position(call.Pos()).String()

				switch {
				// Shape 1: readBool(ctx, reader, "key", def)
				case fn.Name == "readBool" && len(call.Args) >= 4:
					key, ok := stringLit(call.Args[2])
					if !ok {
						return true
					}
					if def, ok := boolLit(call.Args[3]); ok {
						record(key, def, where)
					}

				// Shape 2: parseBool(values["key"], def)
				case fn.Name == "parseBool" && len(call.Args) >= 2:
					idx, ok := call.Args[0].(*ast.IndexExpr)
					if !ok {
						return true
					}
					key, ok := stringLit(idx.Index)
					if !ok {
						return true
					}
					if def, ok := boolLit(call.Args[1]); ok {
						record(key, def, where)
					}
				}
				return true
			})
		}
	}
	return found
}

// boolLit reads a bare `true` / `false` identifier.
func boolLit(e ast.Expr) (bool, bool) {
	ident, ok := e.(*ast.Ident)
	if !ok {
		return false, false
	}
	switch ident.Name {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// resolve returns the single literal default for a key, or ok=false when the
// walk saw none or saw contradictory ones.
func (s boolDefaultSites) resolve(key string) (bool, bool) {
	sites, ok := s[key]
	if !ok || len(sites) != 1 {
		return false, false
	}
	for def := range sites {
		return def, true
	}
	return false, false
}

// minCheckedActivationKeys guards against a vacuous pass: if the walk breaks
// (helpers renamed, argument order changed) it would find nothing and every
// assertion below would be skipped. Measured 2026-08-14: 14 of the projection's
// activation keys resolve to a literal default; re-measured 2026-08-17 over
// guardedBoolKeys (activation keys ∪ the preset vocabulary): 23 of 25. Floor
// set a little below.
const minCheckedActivationKeys = 20

// activationKeys returns each node's on/off key — Keys[0] — skipping the
// unconditional nodes, which have no activation key at all.
func activationKeys() []string {
	out := []string{}
	for _, n := range Nodes() {
		if n.AlwaysOn || len(n.Keys) == 0 {
			continue
		}
		out = append(out, n.Keys[0])
	}
	return out
}

// presetVocabulary returns every key any curated bundle states. Deviations and
// EffectiveChanges resolve an UNSET bundle key through defaultOn
// (matchesBundleValue), so a bundle key whose code default flips without
// defaultOn following it makes the deviation badge and the apply dialog
// under-report — silently, since neither number is derived from anything a test
// currently compares against the call site.
//
// activationKeys alone does not cover them: it takes each node's Keys[0] only,
// and eight of the 21 bundle keys are a node's SECOND-or-later key
// (adaptive_routing_enabled, the four orchestrator gates under
// chat_supervisor_enabled, chat_factuality_verifier_always_run) or sit on an
// AlwaysOn node. All eight are false today, which is exactly why nobody
// noticed — and this repo flips flags to default-ON as kill switches routinely
// (chat_answer_history_*, chat_date_awareness_enabled, factcheck_in_chat).
func presetVocabulary() []string {
	out := []string{}
	for _, p := range Presets() {
		for k := range p.Bundle {
			out = append(out, k)
		}
	}
	return out
}

// guardedBoolKeys is the union of the two, deduplicated and sorted — every
// boolean key whose default the projection or the preset machinery consults
// through defaultOn.
func guardedBoolKeys() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range append(activationKeys(), presetVocabulary()...) {
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDefaultOnMatchesReadBoolDefaults is the guard that makes the defaultOn
// map non-driftable in the direction that matters: what the canvas says about
// a node it has no explicit value for.
func TestDefaultOnMatchesReadBoolDefaults(t *testing.T) {
	sites := collectBoolDefaults(t)

	checked := 0
	for _, key := range guardedBoolKeys() {
		def, ok := sites.resolve(key)
		if !ok {
			if len(sites[key]) > 1 {
				t.Errorf("activation key %q has contradictory literal defaults across "+
					"call sites (%v) — defaultOn can only hold one, so the projection "+
					"cannot be right for both", key, sites[key])
			}
			// No literal default in either read shape (vector's
			// siteConfigParsers registry). Nothing to compare against.
			continue
		}
		checked++
		if defaultOn[key] != def {
			t.Errorf("defaultOn[%q] = %v but the code default is %v (%v). "+
				"The workflow canvas renders this node's state from defaultOn, so "+
				"they must agree — fix project.go's defaultOn map.",
				key, defaultOn[key], def, sites[key][def])
		}
	}

	if checked < minCheckedActivationKeys {
		t.Fatalf("only %d activation keys could be checked (expected >= %d) — the "+
			"AST walk is probably broken, and every assertion above passed "+
			"vacuously", checked, minCheckedActivationKeys)
	}
}

// The other direction: an entry in defaultOn that the code disagrees with is a
// bug even when no node activates on that key today, because boolVal consults
// the map for every key the projection reads (orchestrator gates, the
// adaptive-routing check, the verifier gate).
func TestDefaultOnEntriesMatchTheirCallSites(t *testing.T) {
	sites := collectBoolDefaults(t)

	for key, want := range defaultOn {
		def, ok := sites.resolve(key)
		if !ok {
			continue
		}
		if def != want {
			t.Errorf("defaultOn[%q] = %v but the code default is %v (%v)",
				key, want, def, sites[key][def])
		}
	}
}

// Every key boolVal is asked about outside the node vocabulary — the
// orchestrator gates and the two post-answer gate keys — must also agree with
// its call site. Listed explicitly because these are read by predicate code,
// not by a NodeSpec.Keys entry, so activationKeys() cannot see them.
func TestNonNodeBoolKeysMatchTheirCallSites(t *testing.T) {
	sites := collectBoolDefaults(t)

	keys := []string{
		"adaptive_routing_enabled",
		"chat_corpus_table_enabled",
		"chat_corpus_table_router_llm_enabled",
		"chat_drift_enabled",
		"chat_supervisor_enabled",
		"chat_plan_execute_enabled",
		"chat_agentic_enabled",
		"chat_self_rag_enabled",
		"citation_validation_enabled",
		"chat_factuality_verifier_enabled",
		"chat_factuality_verifier_always_run",
	}

	checked := 0
	for _, key := range keys {
		def, ok := sites.resolve(key)
		if !ok {
			continue
		}
		checked++
		if defaultOn[key] != def {
			t.Errorf("defaultOn[%q] = %v but the code default is %v (%v)",
				key, defaultOn[key], def, sites[key][def])
		}
	}
	if checked < len(keys)-1 {
		t.Fatalf("only %d of %d predicate keys resolved — the AST walk is probably broken",
			checked, len(keys))
	}
}
