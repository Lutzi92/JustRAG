package builtin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/mcp"
)

// defaultBuiltins registers one instance of every built-in tool into a
// registry, mirroring internal/app/routes.go's wiring. Dependencies are nil
// where the constructor only stores them in a handler closure — this test
// reads names, it never invokes a tool. The `_unconfigured` variants stand in
// for the two tools whose production constructor needs a database pool; both
// register under the same name as the configured variant.
func defaultBuiltins(t *testing.T) *mcp.Registry {
	t.Helper()
	r := mcp.NewRegistry()
	r.RegisterBuiltin(NewKbSearch(nil))
	r.RegisterBuiltin(NewKeywordSearch(nil))
	r.RegisterBuiltin(NewCountMentions(nil))
	r.RegisterBuiltin(NewChunkRead(nil))
	r.RegisterBuiltin(NewDocumentOutline(nil))
	r.RegisterBuiltin(NewRecentDocuments(nil, nil, nil))
	r.RegisterBuiltin(NewCalculator())
	r.RegisterBuiltin(NewSQLQueryUnconfigured())
	r.RegisterBuiltin(NewCodeExec(DefaultCodeExecConfig(), nil, nil))
	r.RegisterBuiltin(NewTableQueryUnconfigured())
	r.RegisterBuiltin(NewGraphSearch(nil))
	r.RegisterBuiltin(NewWebSearch(nil))
	r.RegisterBuiltin(NewMemoryRead(nil))
	r.RegisterBuiltin(NewMemoryWrite(nil, nil))
	return r
}

// TestKnownAnswerTools_MatchesRegistry is the W6-R15 cross-check: the names an
// operator may write into chat_answer_tools_by_route must be exactly the names
// the built-in registry knows. Set equality in BOTH directions, so a renamed
// tool (registry has a name the list doesn't) and a stale list entry (list has
// a name the registry doesn't) each fail here rather than silently making an
// allowlist entry unmatchable at answer time.
func TestKnownAnswerTools_MatchesRegistry(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	for _, tool := range defaultBuiltins(t).List("kb-1") {
		registered[tool.Name] = true
	}
	known := map[string]bool{}
	for _, name := range chatpolicy.KnownAnswerTools {
		known[name] = true
	}

	for name := range registered {
		if !known[name] {
			t.Errorf("built-in tool %q is registered but missing from chatpolicy.KnownAnswerTools — "+
				"an operator cannot route-scope it; add it to that list", name)
		}
	}
	for name := range known {
		if !registered[name] {
			t.Errorf("chatpolicy.KnownAnswerTools names %q but no built-in registers it — "+
				"an allowlist entry for it would never match; remove or rename it", name)
		}
	}
	if len(registered) != len(chatpolicy.KnownAnswerTools) {
		t.Errorf("registry has %d built-ins, KnownAnswerTools has %d",
			len(registered), len(chatpolicy.KnownAnswerTools))
	}
}

// TestKnownAnswerTools_MatchesPackageSource is the second half of the guard.
// defaultBuiltins above is a hand-maintained list: a NEW built-in added in a
// new file would be invisible to it and the test above would still pass. This
// arm reads every `mcp.Tool{Name: "..."}` literal out of the package's own
// non-test sources, so adding a tool fails here until both the list and
// chatpolicy.KnownAnswerTools are updated.
func TestKnownAnswerTools_MatchesPackageSource(t *testing.T) {
	t.Parallel()

	names := toolNamesInPackageSource(t)
	if len(names) == 0 {
		t.Fatal("scanned no tool names — the source scan is broken, not the package")
	}

	known := map[string]bool{}
	for _, n := range chatpolicy.KnownAnswerTools {
		known[n] = true
	}
	for _, n := range names {
		if !known[n] {
			t.Errorf("mcp.Tool literal names %q, which chatpolicy.KnownAnswerTools does not list", n)
		}
	}
	inSource := map[string]bool{}
	for _, n := range names {
		inSource[n] = true
	}
	for _, n := range chatpolicy.KnownAnswerTools {
		if !inSource[n] {
			t.Errorf("chatpolicy.KnownAnswerTools names %q, which no mcp.Tool literal in internal/mcp/builtin defines", n)
		}
	}
}

// toolNamesInPackageSource returns the sorted, de-duplicated set of Name
// values of every `mcp.Tool{...}` composite literal in the package's non-test
// files. Two tools (sql_query, table_query) have a configured and an
// unconfigured literal under the same name; de-duplication is why.
func toolNamesInPackageSource(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isMCPToolType(lit.Type) {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Name" {
					continue
				}
				bl, ok := kv.Value.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					// A non-literal name would silently escape this scan;
					// fail loudly rather than under-report.
					t.Errorf("%s: mcp.Tool Name is not a string literal", name)
					continue
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Errorf("%s: cannot unquote %s: %v", name, bl.Value, err)
					continue
				}
				seen[s] = true
			}
			return true
		})
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// isMCPToolType reports whether a composite-literal type is `mcp.Tool`.
func isMCPToolType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Tool" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "mcp"
}
