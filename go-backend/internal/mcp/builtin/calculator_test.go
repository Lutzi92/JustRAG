package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func runCalc(t *testing.T, expr string) (string, error) {
	t.Helper()
	tool := NewCalculator()
	args, _ := json.Marshal(map[string]any{"expression": expr})
	res, err := tool.Handler.Invoke(context.Background(), args)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// TestCalculator_BasicArithmetic walks the four operators on
// integer + float literals plus parens and unary minus. The
// expected results are exact strings (not parsed) — drift in the
// formatter would change LLM-visible output.
//
// Slice (not map) so iteration order is deterministic: a failing
// case lands at a predictable position in CI logs, and `go test
// -run TestCalculator_BasicArithmetic/<subtest>` selects one case.
func TestCalculator_BasicArithmetic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		expr string
		want string
	}{
		{"1 + 2", "3"},
		{"10 - 4", "6"},
		{"3 * 7", "21"},
		{"100 / 4", "25"},
		{"17 % 5", "2"},
		{"(2 + 3) * 4", "20"},
		{"-5 + 8", "3"},
		{"+7", "7"},
		{"1.5 + 2.5", "4"}, // adds to integer 4
		{"1.5 * 2", "3"},
		{"1 / 4.0", "0.25"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			got, err := runCalc(t, tc.expr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCalculator_DenyArbitrary covers the security boundary: any
// non-arithmetic expression node must reject. The deny list is
// closed (only literals + arithmetic ops survive evalArith) so
// the test exercises a sample of forbidden forms.
func TestCalculator_DenyArbitrary(t *testing.T) {
	t.Parallel()
	denied := []string{
		"abs(-5)",                   // function call
		"math.Pi",                   // selector
		"x + 1",                     // identifier
		`"hello"`,                   // string literal
		"true",                      // boolean
		"1 << 2",                    // shift (not in allowed binary ops)
		"1 & 2",                     // bitwise (not allowed)
		"1 == 1",                    // comparison
		"[]int{1,2}",                // composite literal
		"func() int { return 1 }()", // function literal call
	}
	for _, expr := range denied {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			if _, err := runCalc(t, expr); err == nil {
				t.Fatalf("expected error, got nil — security boundary leak")
			}
		})
	}
}

// TestCalculator_DivisionByZero: the calculator returns a clean
// "division by zero" error rather than propagating a panic out of
// constant.BinaryOp. A panicking tool would crash the chat
// goroutine (the MCP dispatcher does not recover), so this is a
// real production-safety property, not just a test ergonomics one.
func TestCalculator_DivisionByZero(t *testing.T) {
	t.Parallel()
	cases := []string{
		"1 / 0",
		"5 % 0",
		"1.0 / 0",
		"1 / (2 - 2)", // computed zero divisor — also must catch
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("calculator panicked on %q: %v (must return a clean error)", expr, r)
				}
			}()
			_, err := runCalc(t, expr)
			if err == nil {
				t.Fatalf("expected division-by-zero error, got nil")
			}
			if !strings.Contains(err.Error(), "division by zero") {
				t.Fatalf("expected error to mention 'division by zero', got %v", err)
			}
		})
	}
}

// TestCalculator_EmptyExpression: empty input is a wiring bug
// (the planner shouldn't dispatch an empty expression). Surface
// as a clean error.
func TestCalculator_EmptyExpression(t *testing.T) {
	t.Parallel()
	_, err := runCalc(t, "")
	if err == nil || !strings.Contains(err.Error(), "expression is required") {
		t.Errorf("expected 'expression is required' error, got %v", err)
	}
}
