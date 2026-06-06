package workflow

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

func evalExpr(t *testing.T, src string, ctx EvalContext) bool {
	t.Helper()
	e, err := ParseExpr(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	v, err := e.Eval(ctx)
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return v
}

func TestExpr_CellFields(t *testing.T) {
	ctx := EvalContext{Cell: model.SourceItem{
		Title: "hotfix login", Priority: "urgent", Type: "bug",
		Labels: []string{"backend", "bug"}, SourceID: "main-plane", State: "todo",
	}}

	cases := []struct {
		src  string
		want bool
	}{
		{`cell.priority == "urgent"`, true},
		{`cell.priority == "low"`, false},
		{`cell.priority != "low"`, true},
		{`cell.type == "bug"`, true},
		{`cell.source == "main-plane"`, true},
		{`cell.state == "todo"`, true},
		{`cell.labels contains "bug"`, true},
		{`cell.labels contains "frontend"`, false},
		{`cell.title matches ".*hotfix.*"`, true},
		{`cell.title matches "^feature"`, false},
	}
	for _, c := range cases {
		if got := evalExpr(t, c.src, ctx); got != c.want {
			t.Errorf("%q = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestExpr_MemoryFields(t *testing.T) {
	ctx := EvalContext{Memory: map[string]string{"complexity": "high", "approach": "jwt"}}
	if !evalExpr(t, `memory.complexity == "high"`, ctx) {
		t.Error("expected memory.complexity == high")
	}
	if evalExpr(t, `memory.complexity == "low"`, ctx) {
		t.Error("expected false")
	}
	// Missing memory key compares as empty string.
	if !evalExpr(t, `memory.missing == ""`, ctx) {
		t.Error("missing key should equal empty string")
	}
	if !evalExpr(t, `memory.approach contains "jw"`, ctx) {
		t.Error("expected substring match on memory value")
	}
}

func TestExpr_StepFields(t *testing.T) {
	ctx := EvalContext{Steps: map[string]StepState{
		"review": {State: "passed", ExitCode: 0, Output: "LGTM, ship it"},
		"build":  {State: "failed", ExitCode: 2},
	}}
	cases := []struct {
		src  string
		want bool
	}{
		{`steps.review.state == "passed"`, true},
		{`steps.build.state == "failed"`, true},
		{`steps.review.exit_code == 0`, true},
		{`steps.build.exit_code == 2`, true},
		{`steps.build.exit_code != 0`, true},
		{`steps.review.output contains "LGTM"`, true},
		{`steps.review.output contains "nope"`, false},
		{`steps.missing.state == ""`, true},
	}
	for _, c := range cases {
		if got := evalExpr(t, c.src, ctx); got != c.want {
			t.Errorf("%q = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestExpr_BooleanCombinators(t *testing.T) {
	ctx := EvalContext{Cell: model.SourceItem{
		Priority: "urgent", Labels: []string{"feature"}, Type: "feature",
	}}
	cases := []struct {
		src  string
		want bool
	}{
		{`cell.labels contains "feature" and cell.priority == "urgent"`, true},
		{`cell.labels contains "feature" and cell.priority == "low"`, false},
		{`cell.labels contains "bug" or cell.type == "feature"`, true},
		{`cell.labels contains "bug" or cell.type == "chore"`, false},
		{`not cell.labels contains "bug"`, true},
		{`not cell.type == "feature"`, false},
		{`(cell.labels contains "bug" or cell.type == "feature") and cell.priority == "urgent"`, true},
		{`cell.labels contains "bug" or cell.type == "feature" and cell.priority == "low"`, false}, // and binds tighter
	}
	for _, c := range cases {
		if got := evalExpr(t, c.src, ctx); got != c.want {
			t.Errorf("%q = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestExpr_PrecedenceAndOverOr(t *testing.T) {
	// false or (true and false) = false ; (false or true) and false = false
	// Use distinct values to detect grouping: A or B and C  ==  A or (B and C)
	ctx := EvalContext{Memory: map[string]string{"a": "1", "b": "1", "c": "0"}}
	// a==1 or (b==1 and c==1) → true or (true and false) → true
	if !evalExpr(t, `memory.a == "1" or memory.b == "1" and memory.c == "1"`, ctx) {
		t.Error("expected and to bind tighter than or → true")
	}
	// (a==0 or b==1) and c==1 → forced grouping → (false or true) and false → false
	if evalExpr(t, `(memory.a == "0" or memory.b == "1") and memory.c == "1"`, ctx) {
		t.Error("expected grouped expression → false")
	}
}

func TestExpr_ParseErrors(t *testing.T) {
	bad := []string{
		`cell.priority =`,              // dangling
		`cell.priority == `,            // missing operand
		`cell.priority "urgent"`,       // missing operator
		`(cell.priority == "x"`,        // unbalanced paren
		`cell.priority == "x" garbage`, // trailing tokens
		`== "x"`,                       // missing accessor
		`cell.priority ~ "x"`,          // unknown operator char
	}
	for _, src := range bad {
		if _, err := ParseExpr(src); err == nil {
			t.Errorf("expected parse error for %q, got nil", src)
		}
	}
}

func TestExpr_EvalErrors(t *testing.T) {
	ctx := EvalContext{Cell: model.SourceItem{Labels: []string{"a"}}}
	// 'matches' on a list is an error.
	e, _ := ParseExpr(`cell.labels matches ".*"`)
	if _, err := e.Eval(ctx); err == nil {
		t.Error("expected error for matches on list")
	}
	// '==' on a list is an error.
	e, _ = ParseExpr(`cell.labels == "a"`)
	if _, err := e.Eval(ctx); err == nil {
		t.Error("expected error for == on list")
	}
	// unknown cell field.
	e, _ = ParseExpr(`cell.bogus == "x"`)
	if _, err := e.Eval(ctx); err == nil {
		t.Error("expected error for unknown cell field")
	}
	// unknown root.
	e, _ = ParseExpr(`foo.bar == "x"`)
	if _, err := e.Eval(ctx); err == nil {
		t.Error("expected error for unknown accessor root")
	}
}

func TestExpr_SingleQuotes(t *testing.T) {
	ctx := EvalContext{Cell: model.SourceItem{Priority: "high"}}
	if !evalExpr(t, `cell.priority == 'high'`, ctx) {
		t.Error("single-quoted strings should work")
	}
}
