package workflow

import "testing"

func TestExprEventScope(t *testing.T) {
	ctx := EvalContext{Event: map[string]string{
		"kind":               "pr_comment",
		"body":               "@apiary fix the lint errors",
		"author":             "alice",
		"author_association": "MEMBER",
		"pr_number":          "42",
		"pr_url":             "https://github.com/o/r/pull/42",
	}}

	cases := []struct {
		expr string
		want bool
	}{
		{`event.kind == "pr_comment"`, true},
		{`event.kind == "pr_review_approved"`, false},
		{`event.body contains "@apiary"`, true},
		{`event.author == "alice" and event.author_association == "MEMBER"`, true},
		{`event.pr_number == "42"`, true},
	}
	for _, c := range cases {
		expr, err := ParseExpr(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		got, err := expr.Eval(ctx)
		if err != nil {
			t.Fatalf("eval %q: %v", c.expr, err)
		}
		if got != c.want {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
}

// A nil event scope (item-triggered instance) resolves event fields as missing
// ("") rather than erroring, so shared workflows stay usable on both axes.
func TestExprEventScope_MissingOnItemInstances(t *testing.T) {
	expr, err := ParseExpr(`event.kind == ""`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := expr.Eval(EvalContext{})
	if err != nil {
		t.Fatalf("eval with nil event: %v", err)
	}
	if !got {
		t.Error("event.kind must resolve as missing (\"\") on item-triggered instances")
	}
}

func TestExprEventScope_UnknownFieldErrors(t *testing.T) {
	expr, err := ParseExpr(`event.nope == "x"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expr.Eval(EvalContext{Event: map[string]string{}}); err == nil {
		t.Error("unknown event field must error, not silently evaluate")
	}
}
