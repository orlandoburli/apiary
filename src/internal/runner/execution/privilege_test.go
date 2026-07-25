package execution

import (
	"testing"
)

func TestIsEnvDenied(t *testing.T) {
	tests := []struct {
		key      string
		denylist []string
		want     bool
	}{
		{"AWS_SECRET_ACCESS_KEY", []string{"AWS_"}, true},
		{"aws_access_key_id", []string{"AWS_"}, true},  // case-insensitive
		{"GITHUB_TOKEN", []string{"GITHUB_TOKEN"}, true}, // exact match (no trailing _)
		{"GITHUB_TOKEN_ENGINEER", []string{"GITHUB_TOKEN"}, true}, // prefix match
		{"HOME", []string{"AWS_"}, false},
		{"PATH", []string{"AWS_", "GITHUB_"}, false},
		{"", []string{"AWS_"}, false},
		{"AWS_PROFILE", []string{}, false}, // empty denylist
	}
	for _, tc := range tests {
		got := isEnvDenied(tc.key, tc.denylist)
		if got != tc.want {
			t.Errorf("isEnvDenied(%q, %v) = %v, want %v", tc.key, tc.denylist, got, tc.want)
		}
	}
}

func TestFilteredEnv(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/root",
		"AWS_ACCESS_KEY_ID=AKIA...",
		"AWS_SECRET_ACCESS_KEY=secret",
		"GITHUB_TOKEN=ghp_xxx",
		"APIARY_LOG=debug",
	}

	t.Run("strips matching prefixes", func(t *testing.T) {
		got := filteredEnv(environ, []string{"AWS_", "GITHUB_"})
		for _, kv := range got {
			if kv == "AWS_ACCESS_KEY_ID=AKIA..." || kv == "AWS_SECRET_ACCESS_KEY=secret" || kv == "GITHUB_TOKEN=ghp_xxx" {
				t.Errorf("filteredEnv: denied variable %q survived denylist", kv)
			}
		}
		// Allowed variables must survive.
		found := map[string]bool{}
		for _, kv := range got {
			found[kv] = true
		}
		for _, want := range []string{"PATH=/usr/bin:/bin", "HOME=/root", "APIARY_LOG=debug"} {
			if !found[want] {
				t.Errorf("filteredEnv: allowed variable %q was incorrectly stripped", want)
			}
		}
	})

	t.Run("empty denylist returns original slice", func(t *testing.T) {
		got := filteredEnv(environ, nil)
		if len(got) != len(environ) {
			t.Errorf("filteredEnv with empty denylist: got %d entries, want %d", len(got), len(environ))
		}
	})

	t.Run("value part containing = is handled correctly", func(t *testing.T) {
		env := []string{"SAFE_VAR=a=b=c", "AWS_KEY=value=extra"}
		got := filteredEnv(env, []string{"AWS_"})
		if len(got) != 1 || got[0] != "SAFE_VAR=a=b=c" {
			t.Errorf("filteredEnv: unexpected result %v", got)
		}
	})
}
