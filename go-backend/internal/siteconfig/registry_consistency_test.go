package siteconfig

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file guards against the exact defect class that motivated it: a
// sibling package (internal/pipeline) once shipped a node claiming
// "mmr_enabled", a key that does not exist anywhere in the codebase. Nothing
// caught it until code review. kbConfigRegistry has the identical exposure
// with worse consequences — a registry key nothing reads is a control a KB
// admin can set, that validates (Validate), that persists (PutSettings),
// and that silently does nothing.
//
// The technique (AST walk over the read-helper call sites) is lifted from
// internal/pipeline/coverage_test.go's collectConfigKeys, which is a working,
// reviewed precedent for exactly this shape of guard. This file reimplements
// it rather than importing it (that one is a _test.go in another package)
// over a different, wider file set, with a fifth route this package's own
// keys required (see below), for a different assertion (registry ⊆ read,
// not drawn ⊆ read).

// configReadHelpers are the four typed accessor functions defined in
// internal/chat/siteconfig.go through which most site_config keys are read.
// In all four the key is Args[2] (0-indexed: ctx, reader, key, ...). Only
// readBool/readInt/readFloat additionally carry a default at Args[3];
// readString has exactly 3 args and no default. We only ever assume
// Args[2] exists (guarded by len(Args) >= 3 below), so that asymmetry needs
// no special-casing.
var configReadHelpers = map[string]FieldType{
	"readBool":   FieldBool,
	"readInt":    FieldInt,
	"readFloat":  FieldFloat,
	"readString": FieldString, // FieldEnum also reads via readString; see isTypeConsistent.
}

// keyShaped filters out incidental string literals (SQL fragments, log
// labels, map values, etc.) that appear in the same composite literals as
// real config keys — same filter internal/pipeline's precedent uses.
var keyShaped = regexp.MustCompile(`^[a-z][a-z0-9_]{3,}$`)

// scannedPackages are the packages read on the answering + ingestion paths
// that between them read every current per-KB registry key. internal/chat
// hosts the four typed read helpers and most call sites; internal/vector
// hosts its own per-key parser registry (siteConfigParsers, route 3) with
// zero calls to the four helpers; internal/processor is needed because
// ingestion keys (contextual_enrichment, kg_extraction_enabled) are read
// there — confirmed by grep, not assumed (see route 5 below, which exists
// specifically because those two keys are read by neither the four helpers,
// a "*Keys" slice, nor the siteConfigParsers map: internal/processor calls
// reader.GetSiteConfigValue(ctx, "<literal>") directly). internal/pipeline
// was added because it is a genuine registry-key reader too (the
// workflow-canvas projection, project.go's Project): it resolves every
// per-KB config key any node references. project.go's own read calls do
// NOT literally match any of the five routes below — it reads keys through
// its unexported boolVal(vals, "<literal>") helper and the batch
// r.GetSiteConfigValues(ctx, keys) (plural, deliberately excluded from
// route 5, see that route's doc comment), neither of which the walk
// recognizes — so adding internal/pipeline alone left len(found) unchanged
// (confirmed empirically at the time: still 185, every key project.go's
// node vocabulary needs already had a recognized read site in
// internal/chat or internal/vector). preset_base.go's PresetBaseFor is
// different: it calls r.GetSiteConfigValue(ctx, "workflow_preset")
// directly with the key as a string literal, deliberately in route-5 shape
// (its own doc comment explains why), specifically so this guard would see
// a real read site for workflow_preset instead of needing knownUnread. That
// is the one new key the walk picks up now — see minExpectedKeys below.
//
// Each package directory is walked non-recursively (os.ReadDir, matching
// the pipeline precedent): internal/processor/raptor and
// internal/vector/testdata are real subdirectories, but neither contains a
// site_config read (confirmed by grep for GetSiteConfigValue/SiteConfig
// across internal/processor/raptor/*.go — no hits), so recursing would add
// walk complexity with zero coverage benefit for the current registry.
var scannedPackages = []string{
	filepath.Join("..", "chat"),
	filepath.Join("..", "vector"),
	filepath.Join("..", "processor"),
	filepath.Join("..", "pipeline"),
	// internal/contentgen reads workspace_analysis_presets and
	// workspace_comparison_presets over Route 5
	// (reader.GetSiteConfigValue(ctx, "<literal>")). The four typed helpers
	// are private to internal/chat, so internal/contentgen cannot call them,
	// and neither key belongs in a "*Keys" slice or a siteConfigParsers map
	// either — same reasoning as internal/processor's route-5 entry above.
	filepath.Join("..", "contentgen"),
}

// minExpectedKeys guards against a silently broken parse. If a refactor
// renames the helpers, changes their argument order, renames
// ResolveFastTierModel, or renames GetSiteConfigValue, the walk would find
// far fewer keys (in the limit, 0) and every assertion below would pass
// vacuously — the exact failure mode this file exists to prevent.
//
// Measured 2026-08-14 via collectConfigKeys() across internal/chat +
// internal/vector + internal/processor + internal/pipeline (all five
// routes, see the doc comment on collectConfigKeys): 186 distinct keys —
// 185 as originally measured for this four-package scan, plus workflow_preset
// itself once preset_base.go's PresetBaseFor gave it a route-5-shaped read
// site (see scannedPackages' doc comment). Floor stays 166 (~10.75% below
// 186, still comfortably under the ~10% convention used elsewhere in this
// file).
const minExpectedKeys = 166

// collectConfigKeys walks scannedPackages and returns every site_config key
// it can see, via five routes:
//
//  1. Calls to the four typed read helpers (configReadHelpers), key at
//     Args[2]. Defined in and mostly called from internal/chat.
//  2. String elements of []string composite literals bound to an
//     identifier whose name ends in "Keys" — e.g. cragConfigKeys in
//     internal/chat/service.go, queryCacheConfigKeys in
//     internal/vector/query_cache_config.go.
//  3. String keys of map[string]siteConfigSetter composite literals — the
//     per-key parser registry in internal/vector/search_siteconfig.go
//     (siteConfigParsers), the SOLE route through which most
//     internal/vector keys (mmr_lambda, recency_boost_enabled,
//     rerank_blend_alpha*, top_n_*, bm25_*, hybrid_dynamic_alpha_*, …) are
//     read. Matched on the map's VALUE type (siteConfigSetter) rather than
//     the variable name, since that is the actual, non-coincidental signal
//     that a map literal is this read route and not an unrelated
//     map[string]bool/int/any literal elsewhere in the same packages.
//  4. Calls to ResolveFastTierModel(ctx, reader, "<key>") — a thin wrapper
//     around readString (internal/chat/siteconfig.go), key at Args[2].
//     Matched on both bare-Ident calls (within internal/chat itself) and
//     qualified SelectorExpr calls (chat.ResolveFastTierModel(...) from
//     internal/processor and other packages).
//  5. Calls to <reader>.GetSiteConfigValue(ctx, "<key>") directly (not
//     through any of the four helpers), key at Args[1]. This route exists
//     because internal/processor reads contextual_enrichment and
//     kg_extraction_enabled (both current registry keys) this way — those
//     two typed helpers are private to internal/chat, so internal/processor
//     cannot call them, and neither key appears in a "*Keys" slice or a
//     siteConfigParsers map either. Confirmed by:
//     grep -n '"contextual_enrichment"\|"kg_extraction_enabled"' internal/chat/siteconfig.go
//     (zero hits) vs.
//     grep -n 'GetSiteConfigValue(ctx, "' internal/processor/processor.go
//     (both present). Matched on method name GetSiteConfigValue (singular)
//     specifically, so the batch GetSiteConfigValues(ctx, keys) call is not
//     mistaken for this route.
//
// Route 5 additionally catches the single direct-call site in
// internal/vector (rerank_blend_alpha's one-shot default-notice log,
// search_siteconfig.go) — harmless, since that key is also seen via route
// 3.
//
// Missing any route makes TestEveryRegistryKeyIsRead report false failures
// (a real per-KB key looking unread) and TestRegistryTypeMatchesReader
// under-check (a key silently skipped because the walk never saw it typed).
func collectConfigKeys(t *testing.T) (found map[string]bool, typedAs map[string]FieldType) {
	t.Helper()
	found = map[string]bool{}
	typedAs = map[string]FieldType{}

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
				switch node := n.(type) {
				case *ast.CallExpr:
					// Route 1: the four typed read helpers, bare Ident call
					// (readBool(ctx, reader, "key", ...) etc).
					if ident, ok := node.Fun.(*ast.Ident); ok {
						if ft, isHelper := configReadHelpers[ident.Name]; isHelper && len(node.Args) >= 3 {
							if key, ok := stringLit(node.Args[2]); ok {
								found[key] = true
								typedAs[key] = ft
							}
							return true
						}
						// Route 4 (bare call form): ResolveFastTierModel(ctx, reader, "key").
						if ident.Name == "ResolveFastTierModel" && len(node.Args) >= 3 {
							if key, ok := stringLit(node.Args[2]); ok {
								found[key] = true
								typedAs[key] = FieldString
							}
							return true
						}
					}
					// Route 4 (qualified call form): chat.ResolveFastTierModel(ctx, reader, "key").
					if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
						if sel.Sel.Name == "ResolveFastTierModel" && len(node.Args) >= 3 {
							if key, ok := stringLit(node.Args[2]); ok {
								found[key] = true
								typedAs[key] = FieldString
							}
							return true
						}
						// Route 5: <reader>.GetSiteConfigValue(ctx, "key") direct call.
						if sel.Sel.Name == "GetSiteConfigValue" && len(node.Args) >= 2 {
							if key, ok := stringLit(node.Args[1]); ok {
								found[key] = true
								// Untyped: hand-rolled parsing at the call site, no
								// single typed helper to check the registry Type against.
							}
							return true
						}
					}

				// Route 2: []string{...} bound to an identifier ending in "Keys".
				case *ast.ValueSpec:
					for i, nameIdent := range node.Names {
						if i >= len(node.Values) {
							continue
						}
						comp, ok := node.Values[i].(*ast.CompositeLit)
						if !ok {
							continue
						}
						if strings.HasSuffix(nameIdent.Name, "Keys") {
							if _, ok := comp.Type.(*ast.ArrayType); ok {
								for _, elt := range comp.Elts {
									if key, ok := stringLit(elt); ok && keyShaped.MatchString(key) {
										found[key] = true
										// Untyped: a batch key list, no per-key type signal.
									}
								}
							}
						}
					}

				// Route 3: map[string]siteConfigSetter{...} composite literals.
				case *ast.CompositeLit:
					mapType, ok := node.Type.(*ast.MapType)
					if !ok {
						return true
					}
					valueIdent, ok := mapType.Value.(*ast.Ident)
					if !ok || valueIdent.Name != "siteConfigSetter" {
						return true
					}
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if key, ok := stringLit(kv.Key); ok && keyShaped.MatchString(key) {
							found[key] = true
							// Untyped: the setter closure parses ad hoc; no single
							// FieldType maps onto "however this closure feels like
							// parsing v".
						}
					}
				}
				return true
			})
		}
	}
	return found, typedAs
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// knownUnread lists registry keys the walk (routes 1-5, across
// internal/chat + internal/vector + internal/processor) cannot find any
// read site for. Each entry MUST carry the grep evidence establishing that,
// not a guess. These are phantom registry entries in the same sense as the
// "mmr_enabled" node the pipeline package once shipped: a KB admin can set
// them, they validate, they persist — and nothing consults them. This task
// does not remove them; removal is a call for whoever owns the registry
// surface (the next two tasks add 17 more keys on top of this file, so this
// map should be empty or shrinking, not growing).
//
// An entry here is checked in BOTH directions by TestEveryRegistryKeyIsRead:
// a key with no read site stays silent (logged), but a key that IS found by
// the walk while still listed here FAILS the test. Otherwise an exemption
// added for a real gap keeps silencing the guard for that key long after the
// gap closed — a stale exemption re-arms silently.
var knownUnread = map[string]string{}

// TestEveryRegistryKeyIsRead is the anti-phantom-key guard. Every key in
// kbConfigRegistry (siteconfig.All()) must be read by production code
// somewhere in internal/chat, internal/vector, or internal/processor —
// otherwise a KB admin can set a per-KB override that validates, persists,
// and does nothing.
func TestEveryRegistryKeyIsRead(t *testing.T) {
	found, _ := collectConfigKeys(t)

	// Floor check FIRST, before any per-key loop: a broken parse must fail
	// loudly, never pass vacuously (a walk that silently finds 0 keys would
	// make every assertion below trivially true).
	if len(found) < minExpectedKeys {
		t.Fatalf("AST walk found only %d config keys across %v (expected >= %d) — "+
			"the parse is probably broken. Check that the four helpers in "+
			"configReadHelpers, ResolveFastTierModel, and GetSiteConfigValue are "+
			"still called with the key as a string literal at the expected "+
			"argument position, and that batch key slices still end in \"Keys\".",
			len(found), scannedPackages, minExpectedKeys)
	}

	for _, fld := range All() {
		if found[fld.Key] {
			// Staleness check: an exemption that is no longer needed must
			// fail, not quietly re-arm. A knownUnread entry silences the
			// phantom-key guard for that key forever; if the walk can now
			// see a read site (a route was added, or the key genuinely
			// gained one), the exemption is dead weight that would also
			// mask a LATER regression for the same key.
			if reason, ok := knownUnread[fld.Key]; ok {
				t.Errorf("registry key %q IS read in %v, but is still listed in knownUnread "+
					"(%q) — delete the knownUnread entry; a stale exemption silences this "+
					"guard for that key permanently.",
					fld.Key, scannedPackages, reason)
			}
			continue
		}
		if reason, ok := knownUnread[fld.Key]; ok {
			t.Logf("registry key %q has no read site in %v; tracked as knownUnread: %s",
				fld.Key, scannedPackages, reason)
			continue
		}
		t.Errorf("registry key %q (group %q) is not read anywhere in %v — one of three "+
			"causes, in the order worth checking: (1) collectConfigKeys is missing a "+
			"read route (see the five documented routes on that function); (2) the key "+
			"is read from a package not listed in scannedPackages, in which case add "+
			"that package to scannedPackages rather than exempting the key; (3) this is "+
			"a genuine phantom registry entry (a control a KB admin can set that does "+
			"nothing). Only for (3) add it to knownUnread with grep evidence — "+
			"knownUnread is the escape hatch of last resort, not the cheap fix.",
			fld.Key, fld.Group, scannedPackages)
	}
}

// TestRegistryTypeMatchesReader cross-checks the registry's declared Type
// against the FieldType implied by the typed helper that actually reads the
// key (routes 1 and 4 only — readBool/readInt/readFloat/readString and
// ResolveFastTierModel, which wraps readString). Routes 2 (batch "*Keys"
// slices) and 3 (the siteConfigParsers map) are exempt by construction:
// both are untyped at the read site — a "*Keys" slice is just a list of
// strings for a batch DB fetch, and each siteConfigSetter closure parses its
// value however it likes (strconv.ParseBool/Atoi/ParseFloat inline), so
// there is no single FieldType to check the registry row against. Route 5
// (direct GetSiteConfigValue calls) is exempt for the same reason: the
// call site hand-parses the raw string itself.
func TestRegistryTypeMatchesReader(t *testing.T) {
	found, typedAs := collectConfigKeys(t)

	if len(found) < minExpectedKeys {
		t.Fatalf("AST walk found only %d config keys (expected >= %d) — parse broken", len(found), minExpectedKeys)
	}

	for _, fld := range All() {
		want, ok := typedAs[fld.Key]
		if !ok {
			// Not seen via a typed route (routes 2/3/5, or unread entirely —
			// TestEveryRegistryKeyIsRead is the test responsible for that).
			continue
		}
		if fld.Type == want {
			continue
		}
		// readString/ResolveFastTierModel also legitimately backs a FieldEnum
		// (e.g. chat_graph_routing_path_mode): the helper just returns the raw
		// string, and Validate()'s Enum check is what constrains it.
		if want == FieldString && fld.Type == FieldEnum {
			continue
		}
		t.Errorf("registry key %q declares Type %q, but the code reads it via a "+
			"helper implying %q — fix the registry row's Type in registry.go "+
			"(or, if the read site is wrong, fix that instead)",
			fld.Key, fld.Type, want)
	}
}
