package config

import (
	"strings"
	"testing"
)

// githubSchema mirrors the real github adapter's declaration. The config
// package cannot import the adapters (dependency direction), so the tests
// inject a schema through the same hook the cli package uses; the adapters'
// own declarations are covered by TestConfigSchemas_MatchConnect in the source
// packages' test files.
func githubSchema() SourceSchema {
	return SourceSchema{
		Keys: []SourceSchemaKey{
			{Name: "repo", Required: true, Desc: `repository in "owner/repo" format`},
			{Name: "api_key", Secret: true, Desc: "GitHub personal access token"},
			{Name: "base_url", Desc: "API base URL, for GitHub Enterprise Server"},
		},
		Aliases: map[string]string{"token": "api_key"},
	}
}

func withSourceSchemas(t *testing.T, schemas map[string]SourceSchema) {
	t.Helper()
	prev := SourceConfigSchema
	SourceConfigSchema = func(sourceType string) (SourceSchema, bool) {
		s, ok := schemas[sourceType]
		return s, ok
	}
	t.Cleanup(func() { SourceConfigSchema = prev })
}

func sourceCfg(sourceType string, cfg map[string]any) *Config {
	return &Config{
		Version: "1",
		Sources: []SourceConfig{{ID: "src", Type: sourceType, Config: cfg}},
	}
}

func TestValidateSourceConfig(t *testing.T) {
	schemas := map[string]SourceSchema{
		"github": githubSchema(),
		"passthrough": {
			Keys:      []SourceSchemaKey{{Name: "endpoint", Required: true, Desc: "backend URL"}},
			OpenEnded: true,
		},
	}

	cases := []struct {
		name     string
		cfg      *Config
		wantErr  string   // substring that must appear
		wantNone []string // substrings that must NOT appear
	}{
		{
			name:    "unknown key",
			cfg:     sourceCfg("github", map[string]any{"repo": "o/r", "nonsense_key": "x"}),
			wantErr: `unknown key "nonsense_key"`,
		},
		{
			name:    "unknown key lists accepted keys",
			cfg:     sourceCfg("github", map[string]any{"repo": "o/r", "nonsense_key": "x"}),
			wantErr: `accepted keys for type "github": api_key, base_url, repo`,
		},
		{
			name:    "alias suggestion token to api_key",
			cfg:     sourceCfg("github", map[string]any{"repo": "o/r", "token": "ghp_x"}),
			wantErr: `unknown key "token" — did you mean "api_key"?`,
		},
		{
			name:    "edit-distance suggestion",
			cfg:     sourceCfg("github", map[string]any{"repo": "o/r", "base_ul": "https://ghe"}),
			wantErr: `unknown key "base_ul" — did you mean "base_url"?`,
		},
		{
			name:    "missing required key",
			cfg:     sourceCfg("github", map[string]any{"api_key": "ghp_x"}),
			wantErr: `missing required key "repo"`,
		},
		{
			// `repo: ${GH_REPO}` on an unset variable reads back as a null
			// value; validate must stay runnable without the hive's secrets,
			// so a written-but-empty key is not "missing".
			name:     "empty value from an unset env var is not missing",
			cfg:      sourceCfg("github", map[string]any{"repo": nil, "api_key": ""}),
			wantNone: []string{"missing required key"},
		},
		{
			name:     "valid config",
			cfg:      sourceCfg("github", map[string]any{"repo": "o/r", "api_key": "ghp_x", "base_url": "https://ghe/api/v3"}),
			wantNone: []string{"config:"},
		},
		{
			name:     "open-ended type accepts unknown keys",
			cfg:      sourceCfg("passthrough", map[string]any{"endpoint": "https://x", "whatever": 1}),
			wantNone: []string{"unknown key"},
		},
		{
			name:    "open-ended type still requires required keys",
			cfg:     sourceCfg("passthrough", map[string]any{"whatever": 1}),
			wantErr: `missing required key "endpoint"`,
		},
		{
			name:     "unregistered source type is not checked",
			cfg:      sourceCfg("mystery", map[string]any{"anything": 1}),
			wantNone: []string{"unknown key", "missing required key"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withSourceSchemas(t, schemas)
			errs := tc.cfg.Validate()
			if tc.wantErr != "" && !hasErrContaining(errs, tc.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", errs, tc.wantErr)
			}
			for _, unwanted := range tc.wantNone {
				if hasErrContaining(errs, unwanted) {
					t.Errorf("Validate() = %v, want no error containing %q", errs, unwanted)
				}
			}
		})
	}
}

// TestValidateSourceConfig_SkippedWhenHookNil mirrors the KnownAdapters
// contract: a config built in code (no adapters registered) must not trip the
// key check.
func TestValidateSourceConfig_SkippedWhenHookNil(t *testing.T) {
	prev := SourceConfigSchema
	SourceConfigSchema = nil
	t.Cleanup(func() { SourceConfigSchema = prev })

	errs := sourceCfg("github", map[string]any{"token": "x"}).Validate()
	if hasErrContaining(errs, "unknown key") {
		t.Errorf("Validate() = %v, want no config-key errors when the hook is nil", errs)
	}
}

// TestValidateSourceConfig_PluginRequiredNotDuplicated: the plugin bridge has a
// dedicated "config.plugin is required" message; the generic required-key check
// must not report the same problem a second time.
func TestValidateSourceConfig_PluginRequiredNotDuplicated(t *testing.T) {
	withSourceSchemas(t, map[string]SourceSchema{
		"plugin": {Keys: []SourceSchemaKey{{Name: "plugin", Required: true, Desc: "plugin instance id"}}},
	})

	errs := sourceCfg("plugin", map[string]any{}).Validate()
	var missing int
	for _, err := range errs {
		if strings.Contains(err.Error(), `missing required key "plugin"`) {
			missing++
		}
	}
	if missing != 0 {
		t.Errorf("Validate() = %v, want the dedicated config.plugin message only", errs)
	}
	if !hasErrContaining(errs, "config.plugin is required") {
		t.Errorf("Validate() = %v, want the dedicated config.plugin message", errs)
	}
}

func TestSuggest(t *testing.T) {
	schema := githubSchema()
	cases := []struct{ key, want string }{
		{"token", "api_key"},    // alias table: too far for edit distance
		{"base_ul", "base_url"}, // one deletion
		{"repos", "repo"},       // one insertion
		{"REPO", "repo"},        // case-insensitive
		{"completely_unrelated", ""},
	}
	for _, tc := range cases {
		if got := schema.suggest(tc.key); got != tc.want {
			t.Errorf("suggest(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}
