package config

import (
	"os"
	"path/filepath"
	"testing"
)

// wdConfig builds a config whose every layer declares a distinct working_dir, so
// a precedence test can drop layers and see which one wins.
func wdConfig(configDir string) *Config {
	return &Config{
		DefaultRunner: "claude",
		Runners: []RunnerConfig{
			{ID: "claude", Config: map[string]any{"working_dir": "/runner/dir"}},
			{ID: "other", Config: map[string]any{"working_dir": "/other/dir"}},
		},
		configDir: configDir,
	}
}

func TestResolveWorkingDir_Precedence(t *testing.T) {
	cfg := wdConfig("/cfg")
	agent := AgentConfig{ID: "dev", Runner: "claude", WorkingDir: "/agent/dir"}

	tests := []struct {
		name     string
		agent    AgentConfig
		wfDir    string
		stepDir  string
		expected string
	}{
		{"step wins over everything", agent, "/wf/dir", "/step/dir", "/step/dir"},
		{"workflow wins over agent and runner", agent, "/wf/dir", "", "/wf/dir"},
		{"agent wins over runner", agent, "", "", "/agent/dir"},
		{
			"runner is used when agent and above are silent",
			AgentConfig{ID: "dev", Runner: "claude"},
			"", "", "/runner/dir",
		},
		{
			"default_runner supplies the runner when the agent names none",
			AgentConfig{ID: "dev"},
			"", "", "/runner/dir",
		},
		{
			"the agent's own runner is preferred over default_runner",
			AgentConfig{ID: "dev", Runner: "other"},
			"", "", "/other/dir",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.ResolveWorkingDir(tc.agent, tc.wfDir, tc.stepDir); got != tc.expected {
				t.Errorf("ResolveWorkingDir = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestResolveWorkingDir_ConfigDirDefault is the #436 regression: with nothing
// configured the step must land in the config's own directory, never in "/".
func TestResolveWorkingDir_ConfigDirDefault(t *testing.T) {
	cfg := &Config{configDir: "/home/dev/project/.apiary"}

	got := cfg.ResolveWorkingDir(AgentConfig{ID: "dev"}, "", "")
	if got != "/home/dev/project/.apiary" {
		t.Errorf("ResolveWorkingDir = %q, want the config directory", got)
	}
	if got == "/" {
		t.Error("ResolveWorkingDir must never fall back to the filesystem root")
	}
}

// TestResolveWorkingDir_NoConfigDirFallsBackToCwd covers a Config built in
// memory (no file on disk): the process working directory stands in, and "/" is
// still never returned.
func TestResolveWorkingDir_NoConfigDirFallsBackToCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	cfg := &Config{}

	if got := cfg.ResolveWorkingDir(AgentConfig{ID: "dev"}, "", ""); got != wd {
		t.Errorf("ResolveWorkingDir = %q, want %q", got, wd)
	}
}

func TestResolveWorkingDir_RelativeResolvedAgainstConfigDir(t *testing.T) {
	cfg := wdConfig("/home/dev/project/.apiary")
	agent := AgentConfig{ID: "dev"}

	if got := cfg.ResolveWorkingDir(agent, "", "../src"); got != "/home/dev/project/src" {
		t.Errorf("step relative dir = %q, want /home/dev/project/src", got)
	}
	if got := cfg.ResolveWorkingDir(agent, "worktrees/main", ""); got != "/home/dev/project/.apiary/worktrees/main" {
		t.Errorf("workflow relative dir = %q, want the config dir joined", got)
	}
}

func TestResolveWorkingDir_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	cfg := wdConfig("/cfg")

	want := filepath.Join(home, "code", "apiary")
	if got := cfg.ResolveWorkingDir(AgentConfig{ID: "dev"}, "", "~/code/apiary"); got != want {
		t.Errorf("ResolveWorkingDir = %q, want %q", got, want)
	}
	if got := cfg.ResolveWorkingDir(AgentConfig{ID: "dev"}, "~", ""); got != filepath.Clean(home) {
		t.Errorf("bare ~ = %q, want %q", got, home)
	}
}

// TestLoad_RecordsConfigDir verifies Load() remembers where the config came
// from, which is what makes the config-dir default work at runtime.
func TestLoad_RecordsConfigDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apiary.yaml")
	body := "version: \"1\"\nagents:\n  - id: dev\n    model: sonnet\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		wantDir = dir
	}
	gotDir, err := filepath.EvalSymlinks(cfg.Dir())
	if err != nil {
		gotDir = cfg.Dir()
	}
	if gotDir != wantDir {
		t.Fatalf("cfg.Dir() = %q, want %q", gotDir, wantDir)
	}

	got := cfg.ResolveWorkingDir(AgentConfig{ID: "dev"}, "", "")
	if gotResolved, err := filepath.EvalSymlinks(got); err == nil {
		got = gotResolved
	}
	if got != wantDir {
		t.Errorf("ResolveWorkingDir = %q, want the config directory %q", got, wantDir)
	}
}

// TestResolveWorkingDir_YAMLPrecedenceEndToEnd parses a real config and checks
// the chain the way an operator would experience it.
func TestResolveWorkingDir_YAMLPrecedenceEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apiary.yaml")
	body := `version: "1"
default_runner: claude
runners:
  - id: claude
    type: cli
    config:
      working_dir: /runner/dir
agents:
  - id: dev
    model: sonnet
    working_dir: /agent/dir
workflows:
  - id: wf
    working_dir: /wf/dir
    steps:
      - id: a
        agent: dev
      - id: b
        agent: dev
        working_dir: /step/dir
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wf := cfg.Workflows[0]
	agent := cfg.Agents[0]

	if agent.WorkingDir != "/agent/dir" {
		t.Errorf("agent working_dir not parsed: %q", agent.WorkingDir)
	}
	if wf.WorkingDir != "/wf/dir" {
		t.Errorf("workflow working_dir not parsed: %q", wf.WorkingDir)
	}
	if got := cfg.ResolveWorkingDir(agent, wf.WorkingDir, wf.Steps[0].WorkingDir); got != "/wf/dir" {
		t.Errorf("step a = %q, want /wf/dir", got)
	}
	if got := cfg.ResolveWorkingDir(agent, wf.WorkingDir, wf.Steps[1].WorkingDir); got != "/step/dir" {
		t.Errorf("step b = %q, want /step/dir", got)
	}
}
