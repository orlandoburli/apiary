package ticket

import (
	"reflect"
	"testing"
)

func TestRefsExtractionAndPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		branch string
		body   string
		want   []string
	}{
		{name: "title", title: "[ask-du] PSP-348 ship it", want: []string{"PSP-348"}},
		{name: "branch", branch: "feat/PSP-348-thing", want: []string{"PSP-348"}},
		{name: "body", body: "Closes #42, tracks PSP-348", want: []string{"PSP-348"}},
		{
			name:   "title then branch then body",
			title:  "CDT-1 fix",
			branch: "feat/PSP-2",
			body:   "also AB-3",
			want:   []string{"CDT-1", "PSP-2", "AB-3"},
		},
		{
			name:   "same key in every field appears once",
			title:  "PSP-348 fix",
			branch: "feat/PSP-348",
			body:   "PSP-348",
			want:   []string{"PSP-348"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Refs(c.title, c.branch, c.body); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Refs(%q, %q, %q) = %v, want %v", c.title, c.branch, c.body, got, c.want)
			}
		})
	}
}

func TestRefsIgnoresNonTicketReferences(t *testing.T) {
	cases := []struct {
		name   string
		title  string
		branch string
		body   string
	}{
		{name: "github closing reference", body: "Closes #42"},
		{name: "github plain reference", body: "see #7"},
		{name: "lowercase branch key", branch: "feat/psp-348-thing"},
		{name: "lowercase token pair", title: "fix utf-8 decoding"},
		{name: "no hyphen", body: "PSP348"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Refs(c.title, c.branch, c.body); len(got) != 0 {
				t.Errorf("Refs(%q, %q, %q) = %v, want none", c.title, c.branch, c.body, got)
			}
		})
	}
}
