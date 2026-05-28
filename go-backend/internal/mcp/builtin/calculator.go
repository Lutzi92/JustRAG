package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"strconv"

	"github.com/justrag/go-backend/internal/mcp"
)

// CalculatorArgs documents the calculator tool input. The expression
// is evaluated as a Go arithmetic constant — that's the standard
// library's calculator: deterministic, no allocations beyond the
// AST walk, and impossible to side-effect (it can't even reach a
// math function — only `+ - * / %` on numeric literals).
type CalculatorArgs struct {
	Expression string `json:"expression"`
}

const calculatorInputSchema = `{
  "type": "object",
  "required": ["expression"],
  "properties": {
    "expression": { "type": "string", "description": "Arithmetic expression. Only numeric literals and the operators + - * / %. No function calls, no variables, no parentheses-only expressions." }
  }
}`

// NewCalculator builds the calculator tool. Backed by go/parser +
// go/constant: the expression is parsed as a Go expression, evaluated
// with the constant package's BinaryOp / UnaryOp, and rejected if it
// contains anything other than numeric literals and arithmetic
// operators. Side-effect-free by construction.
//
// Why not expr-lang/expr or a real expression library: the user-
// supplied expression here always comes from an LLM, which can
// hallucinate function names. Allowing function calls of any kind
// (even pure ones like `abs`) opens a question of "which functions
// are safe?" The current scope — pure arithmetic — has zero
// ambiguity. Extension is a single switch-case away if a future
// eval shows the LLM needs `abs` or `round`.
func NewCalculator() mcp.Tool {
	return mcp.Tool{
		Name:        "calculator",
		Description: "Evaluate an arithmetic expression. Use for date / money / aggregate maths the LLM keeps getting wrong. Only + - * / % on numeric literals.",
		InputSchema: json.RawMessage(calculatorInputSchema),
		Handler:     mcp.ToolHandlerFunc(calculatorHandler),
	}
}

func calculatorHandler(_ context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
	var args CalculatorArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ToolResult{}, fmt.Errorf("calculator: parse args: %w", err)
	}
	if args.Expression == "" {
		return mcp.ToolResult{}, fmt.Errorf("calculator: expression is required")
	}

	expr, err := parser.ParseExpr(args.Expression)
	if err != nil {
		return mcp.ToolResult{}, fmt.Errorf("calculator: parse: %w", err)
	}

	val, err := evalArith(expr)
	if err != nil {
		return mcp.ToolResult{}, fmt.Errorf("calculator: %w", err)
	}

	// Render the result. The Go constant package keeps exact
	// rationals (1/4 stays as the string "1/4" via ExactString),
	// which is the wrong output for an LLM-facing result — it
	// would read "1/4" as a literal instead of as 0.25. Coerce to
	// float64 for any non-integer kind, then strconv-format.
	var rendered string
	switch val.Kind() {
	case constant.Int:
		if v, exact := constant.Int64Val(val); exact {
			rendered = strconv.FormatInt(v, 10)
		} else {
			// Falls through for very large ints constant can't fit
			// in int64. ExactString gives the canonical decimal.
			rendered = val.ExactString()
		}
	case constant.Float:
		f, _ := constant.Float64Val(val)
		rendered = strconv.FormatFloat(f, 'g', -1, 64)
	default:
		rendered = val.ExactString()
	}

	return mcp.ToolResult{
		Text: rendered,
		Meta: map[string]any{
			"expression": args.Expression,
			"result":     rendered,
			"kind":       val.Kind().String(),
		},
	}, nil
}

// evalArith walks the AST and produces a constant.Value. Rejects any
// node type that isn't BasicLit (numeric), BinaryExpr (+ - * / %),
// UnaryExpr (+ -), or ParenExpr. That set is closed and easy to
// audit — anything else (Idents, function calls, selector exprs)
// errors out with the position of the offending node.
func evalArith(node ast.Expr) (constant.Value, error) {
	switch n := node.(type) {
	case *ast.BasicLit:
		switch n.Kind {
		case token.INT, token.FLOAT:
			return constant.MakeFromLiteral(n.Value, n.Kind, 0), nil
		default:
			return nil, fmt.Errorf("disallowed literal kind: %s", n.Kind)
		}
	case *ast.ParenExpr:
		return evalArith(n.X)
	case *ast.UnaryExpr:
		x, err := evalArith(n.X)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case token.ADD:
			return x, nil
		case token.SUB:
			return constant.UnaryOp(n.Op, x, 0), nil
		default:
			return nil, fmt.Errorf("disallowed unary op: %s", n.Op)
		}
	case *ast.BinaryExpr:
		x, err := evalArith(n.X)
		if err != nil {
			return nil, err
		}
		y, err := evalArith(n.Y)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case token.ADD, token.SUB, token.MUL:
			return constant.BinaryOp(x, n.Op, y), nil
		case token.QUO, token.REM:
			if constant.Sign(y) == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return constant.BinaryOp(x, n.Op, y), nil
		default:
			return nil, fmt.Errorf("disallowed binary op: %s", n.Op)
		}
	default:
		return nil, fmt.Errorf("disallowed expression node: %T", n)
	}
}
