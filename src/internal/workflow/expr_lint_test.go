package workflow

import (
	"strings"
	"testing"
)

// TestExpr_CStyleOperatorHints verifies that the lexer points authors at the
// supported keyword operators when it meets C-style ones (#180).
func TestExpr_CStyleOperatorHints(t *testing.T) {
	cases := []struct{ src, hint string }{
		{`memory.a != "x" && memory.b != "y"`, "use 'and'"},
		{`memory.a == "x" || memory.b == "y"`, "use 'or'"},
		{`! cell.title == "x"`, "use 'not'"},
		{`steps.my-step.state == "passed"`, "hyphenated"},
	}
	for _, tc := range cases {
		_, err := ParseExpr(tc.src)
		if err == nil {
			t.Errorf("expected parse error for %q, got nil", tc.src)
			continue
		}
		if !strings.Contains(err.Error(), tc.hint) {
			t.Errorf("error for %q should mention %q, got: %v", tc.src, tc.hint, err)
		}
	}
}

// TestLintExpr verifies the static lint entry point wired into config.LintExpr.
func TestLintExpr(t *testing.T) {
	good := []string{
		`${{ memory.track != "100x" and memory.track != "decomposed" }}`,
		`memory.a == "x"`, // bare expression, no ${{ }} wrapper
		`${{ steps.lint.state == 'passed' or steps.tests.state == 'passed' }}`,
	}
	for _, src := range good {
		if err := LintExpr(src); err != nil {
			t.Errorf("LintExpr(%q): unexpected error: %v", src, err)
		}
	}
	bad := []string{
		`${{ memory.track != "100x" && memory.track != "decomposed" }}`,
		`memory.a == "x" || memory.b == "y"`,
		`${{ memory.a == }}`,
	}
	for _, src := range bad {
		if err := LintExpr(src); err == nil {
			t.Errorf("LintExpr(%q): expected error, got nil", src)
		}
	}
}
