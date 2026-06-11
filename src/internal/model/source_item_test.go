package model

import "testing"

func TestSourceItemLogLabel(t *testing.T) {
	cases := []struct {
		name   string
		item   SourceItem
		expect string
	}{
		{"jira key differs from id", SourceItem{ID: "10234", Number: "CDT-123"}, "10234 CDT-123"},
		{"github number is the id", SourceItem{ID: "42", Number: "#42"}, "42"},
		{"plane uuid with project key", SourceItem{ID: "9b1c-uuid", Number: "PSP-398"}, "9b1c-uuid PSP-398"},
		{"plane uuid without project identifier", SourceItem{ID: "9b1c-uuid", Number: "#7"}, "9b1c-uuid #7"},
		{"no number", SourceItem{ID: "42"}, "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.LogLabel(); got != tc.expect {
				t.Errorf("LogLabel() = %q, want %q", got, tc.expect)
			}
		})
	}
}
