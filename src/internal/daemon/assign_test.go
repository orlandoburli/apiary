package daemon

import "testing"

func TestParseAssignDirective(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
		wantOK bool
	}{
		{"simple", "Analysis done.\nAPIARY-ASSIGN: engineer", "engineer", true},
		{"with agent prefix", "APIARY-ASSIGN: agent:staff", "staff", true},
		{"case insensitive key", "apiary-assign: PO", "po", true},
		{"last wins", "APIARY-ASSIGN: engineer\nmore\nAPIARY-ASSIGN: reviewer", "reviewer", true},
		{"trailing spaces", "APIARY-ASSIGN:   qa   ", "qa", true},
		{"none", "no directive here", "", false},
		{"empty value", "APIARY-ASSIGN:   ", "", false},
		{"inline not matched", "see APIARY-ASSIGN: engineer here", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseAssignDirective(tt.output)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("parseAssignDirective(%q) = (%q, %v), want (%q, %v)", tt.output, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
