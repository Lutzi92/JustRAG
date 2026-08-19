package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestQuestionRowsAreOnlyInsertedThroughTheSeam is the structural guard for
// the regenerate change: a question row may enter the database from exactly
// one place, resolveTurnUserMessage, which is what decides between inserting a
// new question and reusing the one a regenerate is replacing. Before that seam
// existed, three answer paths each handed Role: "user" straight to
// Store.AddMessage, and a regenerate went through whichever one the turn
// happened to take — which is how the user's prompt ended up in the thread
// twice.
//
// Why a source-level check and not a request-level one: driving any answer
// path far enough to reach the insert needs a live model backend (embedding,
// rerank and completion all sit in front of it), and this package's handler
// harness has no aiResolver — a turn dies before the insert, which makes an
// "exactly one user row" assertion pass whether or not the seam is used. This
// test asserts the invariant that assertion was meant to protect, and it does
// go red: handing a question straight to AddMessage again fails it by name.
func TestQuestionRowsAreOnlyInsertedThroughTheSeam(t *testing.T) {
	const seam = "resolveTurnUserMessage"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	var offenders []string
	questionInserts := 0

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.CompositeLit)
				if !ok || !isAddMessageParams(lit) || roleOf(lit) != "user" {
					continue
				}
				questionInserts++
				if calleeName(call) != seam {
					offenders = append(offenders,
						fset.Position(lit.Pos()).String()+" -> "+calleeName(call))
				}
			}
			return true
		})
	}

	// Without this, deleting every question insert — or renaming the params
	// struct — would leave the test passing over nothing.
	if questionInserts == 0 {
		t.Fatal(`found no AddMessageParams{Role: "user"} anywhere — this guard no longer matches the code it protects`)
	}
	if len(offenders) > 0 {
		t.Errorf("question rows are built for a callee other than %s at %v; route them through %s instead, "+
			"or a regenerate will persist a second copy of the user's question", seam, offenders, seam)
	}
}

func isAddMessageParams(lit *ast.CompositeLit) bool {
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == "AddMessageParams"
}

// calleeName returns the called function's own name, dropping any receiver
// ("h.store.AddMessage" -> "AddMessage").
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	default:
		return ""
	}
}

// roleOf returns the Role field's string value, or "" when it is absent or
// not a string literal.
func roleOf(lit *ast.CompositeLit) string {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Role" {
			continue
		}
		val, ok := kv.Value.(*ast.BasicLit)
		if !ok || val.Kind != token.STRING {
			return ""
		}
		s, err := strconv.Unquote(val.Value)
		if err != nil {
			return ""
		}
		return s
	}
	return ""
}
