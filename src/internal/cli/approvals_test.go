package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

func TestParseFieldFlags(t *testing.T) {
	got, err := parseFieldFlags([]string{"strategy=canary", "note=has=equals", "empty="})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"strategy": "canary", "note": "has=equals", "empty": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %q = %q, want %q", k, got[k], v)
		}
	}

	for _, bad := range []string{"novalue", "=orphan"} {
		if _, err := parseFieldFlags([]string{bad}); err == nil {
			t.Errorf("expected an error for --field %q", bad)
		}
	}
	if _, err := parseFieldFlags([]string{"a=1", "a=2"}); err == nil {
		t.Error("expected an error when the same field is given twice")
	}
}

func TestCoerceApprovalValue(t *testing.T) {
	choice := approvalFieldSpec{Name: "strategy", Type: "choice", Options: []string{"canary", "full"}}
	if v, err := coerceApprovalValue(choice, "canary"); err != nil || v != "canary" {
		t.Fatalf("choice: v=%v err=%v", v, err)
	}
	if _, err := coerceApprovalValue(choice, "sideways"); err == nil {
		t.Error("expected an off-list choice to be rejected")
	}

	number := approvalFieldSpec{Name: "count", Type: "number"}
	if v, err := coerceApprovalValue(number, "42"); err != nil || v != float64(42) {
		t.Fatalf("number: v=%v err=%v", v, err)
	}
	if _, err := coerceApprovalValue(number, "many"); err == nil {
		t.Error("expected a non-numeric number to be rejected")
	}

	boolean := approvalFieldSpec{Name: "urgent", Type: "boolean"}
	for _, yes := range []string{"true", "yes", "y", "1"} {
		if v, err := coerceApprovalValue(boolean, yes); err != nil || v != true {
			t.Errorf("boolean %q: v=%v err=%v", yes, v, err)
		}
	}
	if v, err := coerceApprovalValue(boolean, "no"); err != nil || v != false {
		t.Errorf("boolean no: v=%v err=%v", v, err)
	}
	if _, err := coerceApprovalValue(boolean, "maybe"); err == nil {
		t.Error("expected a non-boolean to be rejected")
	}
}

// Off a terminal, a missing required field must fail loudly rather than block on
// stdin — a scripted approval that hangs is worse than one that errors.
func TestResolveApprovalValuesNonInteractive(t *testing.T) {
	request := &db.ApprovalRequest{
		ID: "wf:gate",
		Fields: []map[string]any{
			{"name": "strategy", "type": "choice", "options": []any{"canary", "full"}, "required": true},
			{"name": "note", "type": "string"},
		},
	}

	// isInteractive() is false under `go test` (stdin is not a char device).
	if _, err := resolveApprovalValues(request, map[string]string{}); err == nil {
		t.Fatal("expected a missing required field to be an error off a terminal")
	} else if !strings.Contains(err.Error(), "--field strategy=") {
		t.Errorf("error should name the flag to pass, got %q", err)
	}

	values, err := resolveApprovalValues(request, map[string]string{"strategy": "canary"})
	if err != nil {
		t.Fatal(err)
	}
	if values["strategy"] != "canary" {
		t.Errorf("strategy = %v, want canary", values["strategy"])
	}
	if _, present := values["note"]; present {
		t.Error("an optional field left unset should be omitted, not sent empty")
	}
}

// A typo'd --field name would otherwise be dropped silently, and the answer would
// look successful while the workflow read a missing value.
func TestResolveApprovalValuesRejectsUnknownField(t *testing.T) {
	request := &db.ApprovalRequest{
		ID:     "wf:gate",
		Fields: []map[string]any{{"name": "strategy", "type": "string"}},
	}
	_, err := resolveApprovalValues(request, map[string]string{"stratagy": "canary"})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected an unknown-field error, got %v", err)
	}
}

// Options survive JSON as []any; the CLI must read both that and the in-memory
// []string form.
func TestApprovalFieldsOfNormalizesOptions(t *testing.T) {
	request := &db.ApprovalRequest{Fields: []map[string]any{
		{"name": "a", "options": []any{"x", "y"}},
		{"name": "b", "options": []string{"z"}},
		{"name": ""}, // nameless fields are skipped
	}}
	fields := approvalFieldsOf(request)
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(fields))
	}
	if strings.Join(fields[0].Options, ",") != "x,y" || fields[1].Options[0] != "z" {
		t.Fatalf("options not normalized: %+v", fields)
	}
	if fields[0].Type != "string" {
		t.Errorf("an unset type should default to string, got %q", fields[0].Type)
	}
}
