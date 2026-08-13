package cli

import "testing"

func TestParseInputPairs(t *testing.T) {
	got, err := parseInputPairs([]string{"scope=q3", "dry_run=true", "note=a=b"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["scope"] != "q3" || got["dry_run"] != "true" {
		t.Errorf("parsed = %v", got)
	}
	// Only the first '=' separates: a value may legitimately contain more.
	if got["note"] != "a=b" {
		t.Errorf("note = %q, want %q", got["note"], "a=b")
	}
}

func TestParseInputPairs_Empty(t *testing.T) {
	got, err := parseInputPairs(nil)
	if err != nil || got != nil {
		t.Fatalf("got (%v, %v), want (nil, nil) so no input field is sent", got, err)
	}
}

func TestParseInputPairs_Invalid(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue"} {
		if _, err := parseInputPairs([]string{bad}); err == nil {
			t.Errorf("parseInputPairs(%q) accepted a malformed pair", bad)
		}
	}
}
