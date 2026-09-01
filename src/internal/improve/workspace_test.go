package improve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func TestRedactConfigBlanksSecrets(t *testing.T) {
	raw := `agents:
  - id: engineer
    model: sonnet
    source_token: ghp_realsecretvalue123
    source_email: a@b.com
    env:
      API_KEY: sk-live-abcdef
      LOG_LEVEL: debug
  - id: qa
    source_token: ${GITHUB_TOKEN_QA}
settings:
  api_key: literal-secret
`
	got := RedactConfig(raw)

	for _, secret := range []string{"ghp_realsecretvalue123", "sk-live-abcdef", "literal-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q survived redaction:\n%s", secret, got)
		}
	}
	// A ${VAR} reference names an environment variable rather than carrying a
	// secret, and the advisor may legitimately reason about which var an agent
	// uses — so it stays.
	if !strings.Contains(got, "${GITHUB_TOKEN_QA}") {
		t.Error("a pure ${VAR} reference should be preserved")
	}
	// Non-secret structure must survive, or the advisor cannot patch the file.
	for _, keep := range []string{"id: engineer", "model: sonnet", "source_email: a@b.com"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction removed non-secret content %q:\n%s", keep, got)
		}
	}

	// Every value inside an env: block is blanked, including innocuous ones like
	// LOG_LEVEL. This over-redacts deliberately: env blocks routinely carry
	// tokens and there is no reliable way to tell from the key name, so the safe
	// default is to blank them all. The KEY NAMES survive, so the advisor can
	// still see which variables a step sets and reason about them — it just
	// never sees a value.
	if strings.Contains(got, "LOG_LEVEL: debug") {
		t.Error("env values should be blanked wholesale, not filtered by key name")
	}
	if !strings.Contains(got, "LOG_LEVEL: <redacted>") {
		t.Errorf("the env key name must survive so the advisor sees what is set:\n%s", got)
	}
}

func TestRedactConfigLeavesEnvBlockAfterDedent(t *testing.T) {
	raw := `agents:
  - id: a
    env:
      SECRET_THING: hunter2
    model: sonnet
workflows:
  - id: w
`
	got := RedactConfig(raw)
	if strings.Contains(got, "hunter2") {
		t.Error("env value inside the block must be redacted")
	}
	// `model` sits at a shallower indent than the env entries, so it must not be
	// swallowed by the env block.
	if !strings.Contains(got, "model: sonnet") {
		t.Errorf("dedent should end the env block:\n%s", got)
	}
}

func TestExcludedPaths(t *testing.T) {
	root := "/repo"
	cases := []struct {
		path string
		want bool
	}{
		{"/repo/apiary.yaml", false},
		{"/repo/.apiary/agents/engineer.md", false},
		{"/repo/.claude/skills/foo/SKILL.md", false},
		{"/repo/.env", true},
		{"/repo/.env.local", true},
		{"/repo/config.env", true},
		{"/repo/my-secret-file.yaml", true},
		{"/repo/aws-credentials", true},
		{"/repo/key.pem", true},
		{"/repo/.git/config", true},
		{"/repo/.apiary/apiary.db", true},
		{"/repo/.apiary/apiary.db-wal", true},
		{"/repo/.apiary/logs/apiary.log", true},
		{"/repo/.apiary/logs/transcripts/t1/a.md", true},
		{"/repo/.apiary/memory/MEMORY.md", true},
		{"/etc/passwd", true},               // outside the root
		{"/repo/../elsewhere/x.yaml", true}, // escapes the root
	}
	for _, tc := range cases {
		if got := Excluded(tc.path, root); got != tc.want {
			t.Errorf("Excluded(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestDiscoverWalksTheWorkspace(t *testing.T) {
	root := t.TempDir()
	mkdir := func(p string) { os.MkdirAll(filepath.Join(root, p), 0o755) }
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(".apiary/agents")
	mkdir(".claude/skills/deploying")
	write(".apiary/apiary.yaml", "version: \"1\"\n")
	write(".apiary/agents/engineer.md", "you are an engineer\n")
	write(".claude/skills/deploying/SKILL.md", "how to deploy\n")

	cfg := &config.Config{Agents: []config.AgentConfig{{
		ID:       "engineer",
		SoulFile: filepath.Join(root, ".apiary/agents/engineer.md"),
		Skills:   []string{"deploying", "nonexistent-skill"},
	}}}

	ws, err := Discover(cfg, filepath.Join(root, ".apiary/apiary.yaml"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	kinds := map[FileKind]int{}
	for _, f := range ws.Files {
		kinds[f.Kind]++
	}
	if kinds[KindConfig] != 1 {
		t.Errorf("want the config file, got %d", kinds[KindConfig])
	}
	if kinds[KindSoul] != 1 {
		t.Errorf("want the soul file, got %d", kinds[KindSoul])
	}
	if kinds[KindSkill] != 1 {
		t.Errorf("want the resolvable skill, got %d", kinds[KindSkill])
	}

	// A skill that cannot be located must be reported, never silently dropped:
	// an advisor reasoning about an agent whose instructions it never saw is
	// worse than one that knows a piece is missing.
	if len(ws.UnresolvedSkills) != 1 || !strings.Contains(ws.UnresolvedSkills[0], "nonexistent-skill") {
		t.Errorf("UnresolvedSkills = %v, want the missing skill reported", ws.UnresolvedSkills)
	}
	if !strings.Contains(ws.UnresolvedSkills[0], "engineer") {
		t.Error("an unresolved skill should name the agent that declared it")
	}
}

func TestDiscoverSkipsExcludedSoulFile(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".apiary"), 0o755)
	os.WriteFile(filepath.Join(root, ".apiary/apiary.yaml"), []byte("version: \"1\"\n"), 0o600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=abc\n"), 0o600)

	cfg := &config.Config{Agents: []config.AgentConfig{{
		ID: "sneaky", SoulFile: filepath.Join(root, ".env"),
	}}}

	ws, err := Discover(cfg, filepath.Join(root, ".apiary/apiary.yaml"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, f := range ws.Files {
		if strings.Contains(f.Content, "TOKEN=abc") {
			t.Fatal(".env content reached the workspace through a soul_file reference")
		}
	}
}

func TestWorkspaceFilterByBreadth(t *testing.T) {
	ws := &Workspace{Files: []WorkspaceFile{
		{Path: "apiary.yaml", Kind: KindConfig},
		{Path: "wf.yaml", Kind: KindWorkflow, Owner: "impl"},
		{Path: "a.md", Kind: KindSoul, Owner: "busy"},
		{Path: "b.md", Kind: KindSoul, Owner: "idle"},
		{Path: "c.md", Kind: KindSkill, Owner: "flagged"},
	}}
	active := map[string]bool{"busy": true, "flagged": true}
	flagged := map[string]bool{"flagged": true}

	all := ws.Filter(BreadthAll, active, flagged)
	if len(all) != 5 {
		t.Errorf("BreadthAll = %d files, want all 5", len(all))
	}

	act := ws.Filter(BreadthActive, active, flagged)
	if len(act) != 4 {
		t.Errorf("BreadthActive = %d files, want 4 (idle agent's soul dropped)", len(act))
	}

	flg := ws.Filter(BreadthFlagged, active, flagged)
	if len(flg) != 3 {
		t.Errorf("BreadthFlagged = %d files, want 3 (config, workflow, flagged skill)", len(flg))
	}
	// Config and workflow files are always in scope — they are what gets patched.
	for _, f := range flg {
		if f.Kind == KindSoul {
			t.Error("BreadthFlagged should not carry an unflagged agent's soul")
		}
	}
}
