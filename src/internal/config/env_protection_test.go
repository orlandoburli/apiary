package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandEnvBraced verifies the braced-only expansion used for env values:
// the documented `${VAR}` forwarding still works while shell-local `$VAR`,
// positional `$3` and `$$` are left for the agent's shell.
func TestExpandEnvBraced(t *testing.T) {
	t.Setenv("APIARY_TEST_TOKEN", "secret123")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"braced ref", "${APIARY_TEST_TOKEN}", "secret123"},
		{"spaced braced ref", "${ APIARY_TEST_TOKEN }", "secret123"},
		{"unset braced ref blanks", "${APIARY_TEST_UNSET}", ""},
		{"bare ref preserved", "$APIARY_TEST_TOKEN", "$APIARY_TEST_TOKEN"},
		{"positional preserved", "$3", "$3"},
		{"escaped dollar preserved", "$$", "$$"},
		{"expression preserved", `${{ input.dry_run }}`, `${{ input.dry_run }}`},
		{"mixed", `X=$APIARY_TEST_TOKEN Y=${APIARY_TEST_TOKEN}`, `X=$APIARY_TEST_TOKEN Y=secret123`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandEnvBraced(tc.in); got != tc.want {
				t.Errorf("expandEnvBraced(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLoad_ProtectsAgentPromptText is the Part B #526 repro: config-load env
// expansion must not blank shell variables inside prompt/summary_prompt text,
// while source credentials and braced env forwarding still expand.
func TestLoad_ProtectsAgentPromptText(t *testing.T) {
	t.Setenv("APIARY_TEST_TOKEN", "secret123")
	// APIARY_MEMORY_DIR is deliberately absent from the daemon env: it is set
	// per agent run, so load-time expansion would blank it.
	os.Unsetenv("APIARY_MEMORY_DIR")

	dir := t.TempDir()
	path := filepath.Join(dir, "apiary.yaml")
	prompt := `LOG="$APIARY_MEMORY_DIR/global/x.md"; mkdir -p "$(dirname "$LOG")"; ` +
		`jq -c "{commit_id:$sha, body:$body}" "$3"; printf $$; echo ${{ input.dry_run }}`
	summary := `summarize $HOME in $APIARY_MEMORY_DIR`

	raw := `version: "1"
sources:
  - id: gh
    type: github
    config:
      api_key: ${APIARY_TEST_TOKEN}
agents:
  - id: a
    model: m
    env:
      BRACED: ${APIARY_TEST_TOKEN}
      SHELL: $APIARY_TEST_TOKEN
workflows:
  - id: w
    env:
      DEPLOY_URL: "${APIARY_TEST_TOKEN}"
    steps:
      - id: s
        agent: a
        prompt: '` + prompt + `'
        summary_prompt: '` + summary + `'
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Sources[0].Config["api_key"]; got != "secret123" {
		t.Errorf("source api_key = %v, want secret123 (${VAR} must still expand)", got)
	}
	if got := cfg.Agents[0].Env["BRACED"]; got != "secret123" {
		t.Errorf("agent env BRACED = %q, want secret123", got)
	}
	if got := cfg.Agents[0].Env["SHELL"]; got != "$APIARY_TEST_TOKEN" {
		t.Errorf("agent env SHELL = %q, want bare $VAR preserved", got)
	}
	if got := cfg.Workflows[0].Env["DEPLOY_URL"]; got != "secret123" {
		t.Errorf("workflow env DEPLOY_URL = %q, want secret123", got)
	}
	if got := cfg.Workflows[0].Steps[0].Prompt; got != prompt {
		t.Errorf("prompt was modified at load:\n got: %q\nwant: %q", got, prompt)
	}
	if got := cfg.Workflows[0].Steps[0].SummaryPrompt; got != summary {
		t.Errorf("summary_prompt was modified at load:\n got: %q\nwant: %q", got, summary)
	}
}
