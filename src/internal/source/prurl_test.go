package source_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/source"
)

func TestParsePullRequestURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"github", "https://github.com/acme/widgets/pull/42", 42},
		{"github with sub-path", "https://github.com/acme/widgets/pull/42/files", 42},
		{"codeberg", "https://codeberg.org/acme/widgets/pulls/7", 7},
		{"gitlab", "https://gitlab.com/acme/group/widgets/-/merge_requests/13", 13},
		{"bitbucket", "https://bitbucket.org/acme/widgets/pull-requests/5", 5},
		{"self-hosted", "https://git.internal.example/acme/widgets/pull/1", 1},
		{"repo named pull", "https://github.com/acme/pull/pull/9", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, err := source.ParsePullRequestURL(tc.raw)
			if err != nil {
				t.Fatalf("ParsePullRequestURL(%q): %v", tc.raw, err)
			}
			if pr.Number != tc.want {
				t.Errorf("number = %d, want %d", pr.Number, tc.want)
			}
			if pr.URL != tc.raw {
				t.Errorf("url = %q, want %q", pr.URL, tc.raw)
			}
		})
	}
}

func TestParsePullRequestURL_Rejects(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"empty", ""},
		{"not a url", "not a url"},
		{"no pull segment", "https://github.com/acme/widgets/issues/42"},
		{"no number", "https://github.com/acme/widgets/pull/abc"},
		{"zero", "https://github.com/acme/widgets/pull/0"},
		{"non-http scheme", "ssh://git@github.com/acme/widgets/pull/42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if pr, err := source.ParsePullRequestURL(tc.raw); err == nil {
				t.Errorf("expected an error for %q, got %+v", tc.raw, pr)
			}
		})
	}
}
