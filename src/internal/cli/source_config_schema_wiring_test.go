package cli

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"

	// Register the real source adapters, as cmd/apiary does, so the schema
	// probe in init() reads actual adapter declarations.
	_ "github.com/orlandoburli/apiary/internal/source/dynatrace"
	_ "github.com/orlandoburli/apiary/internal/source/github"
	_ "github.com/orlandoburli/apiary/internal/source/jira"
	_ "github.com/orlandoburli/apiary/internal/source/plane"
	_ "github.com/orlandoburli/apiary/internal/source/pluginsource"
	_ "github.com/orlandoburli/apiary/internal/source/prometheus"
)

// TestSourceConfigSchemaWiring_RealAdapters pins the key set every built-in
// source declares. The lists are derived from each adapter's Connect; if a new
// key is read there without being declared here, `apiary validate` would reject
// a config that actually works, so this test is the place that breaks first.
func TestSourceConfigSchemaWiring_RealAdapters(t *testing.T) {
	if config.SourceConfigSchema == nil {
		t.Fatal("config.SourceConfigSchema not wired by cli init()")
	}

	want := map[string]struct {
		keys     []string
		required []string
	}{
		"github": {
			keys:     []string{"api_key", "base_url", "repo"},
			required: []string{"repo"},
		},
		"plane": {
			keys:     []string{"api_key", "base_url", "project", "workspace"},
			required: []string{"api_key", "project", "workspace"},
		},
		"jira": {
			keys:     []string{"api_token", "base_url", "email", "project", "started_state"},
			required: []string{"api_token", "base_url", "email"},
		},
		"prometheus": {
			keys: []string{"ack_via_silence", "alertmanager_url", "basic_auth_password", "basic_auth_user",
				"bearer_token", "dispatch_by", "max_new_per_poll", "min_age", "silence_duration"},
			required: []string{"alertmanager_url"},
		},
		"dynatrace": {
			keys:     []string{"api_token", "base_url", "lookback", "max_new_per_poll", "min_age"},
			required: []string{"api_token", "base_url"},
		},
		"plugin": {
			keys:     []string{"plugin"},
			required: []string{"plugin"},
		},
	}

	for sourceType, exp := range want {
		schema, ok := config.SourceConfigSchema(sourceType)
		if !ok {
			t.Errorf("%s: no config schema declared", sourceType)
			continue
		}
		if schema.OpenEnded {
			t.Errorf("%s: OpenEnded = true, want a closed key set", sourceType)
		}
		var keys, required []string
		for _, k := range schema.Keys {
			keys = append(keys, k.Name)
			if k.Required {
				required = append(required, k.Name)
			}
		}
		if !sameSet(keys, exp.keys) {
			t.Errorf("%s: keys = %v, want %v", sourceType, keys, exp.keys)
		}
		if !sameSet(required, exp.required) {
			t.Errorf("%s: required keys = %v, want %v", sourceType, required, exp.required)
		}
	}

	if _, ok := config.SourceConfigSchema("no-such-adapter"); ok {
		t.Error("unknown source type reported a schema, want none (check skipped)")
	}
}

// TestSourceConfigSchemaWiring_ValidateRejectsTokenTypo is the end-to-end
// acceptance check for issue #441: `apiary validate` (cli wiring + real
// adapters) must reject `token:` on a github source and point at `api_key`,
// instead of letting every poll run unauthenticated.
func TestSourceConfigSchemaWiring_ValidateRejectsTokenTypo(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{
			ID:     "gh",
			Type:   "github",
			Config: map[string]any{"repo": "my-org/my-repo", "token": "ghp_secret"},
		}},
	}

	var found string
	for _, e := range cfg.Validate() {
		if strings.Contains(e.Error(), `unknown key "token"`) {
			found = e.Error()
		}
	}
	if found == "" {
		t.Fatalf("Validate() accepted config.token on a github source: %v", cfg.Validate())
	}
	for _, want := range []string{`sources[0] "gh"`, `did you mean "api_key"`, `accepted keys for type "github"`} {
		if !strings.Contains(found, want) {
			t.Errorf("error %q missing %q", found, want)
		}
	}
	if strings.Contains(found, "ghp_secret") {
		t.Errorf("error %q echoes the secret value", found)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}
