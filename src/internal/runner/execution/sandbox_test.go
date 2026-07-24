package execution

import (
	"os"
	"strings"
	"testing"
)

func TestFilteredHostEnv_ExcludesSecrets(t *testing.T) {
	// Simulate a daemon environment that leaks API keys.
	t.Setenv("ANTHROPIC_API_KEY", "secret-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_leaked")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := filteredHostEnv(nil)

	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN":
			t.Errorf("secret variable %q must not be inherited by agent subprocess", k)
		}
	}
}

func TestFilteredHostEnv_PreservesAllowList(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/runner")

	env := filteredHostEnv(nil)

	found := map[string]bool{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		found[k] = true
	}
	for _, want := range []string{"PATH", "HOME"} {
		if !found[want] {
			t.Errorf("expected allowlisted variable %q to be present", want)
		}
	}
}

func TestFilteredHostEnv_OverlayTakesPrecedence(t *testing.T) {
	t.Setenv("HOME", "/home/daemon")
	overlay := map[string]string{
		"GITHUB_TOKEN":    "scoped-token",
		"ANTHROPIC_API_KEY": "task-key",
	}

	env := filteredHostEnv(overlay)

	found := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		found[k] = v
	}
	if found["GITHUB_TOKEN"] != "scoped-token" {
		t.Errorf("overlay GITHUB_TOKEN not present or wrong: %q", found["GITHUB_TOKEN"])
	}
	if found["ANTHROPIC_API_KEY"] != "task-key" {
		t.Errorf("overlay ANTHROPIC_API_KEY not present or wrong: %q", found["ANTHROPIC_API_KEY"])
	}
}

func TestCliSandbox_WrapCommand(t *testing.T) {
	s := &cliSandbox{
		image:   "ghcr.io/example/agent:latest",
		user:    "nobody",
		network: "none",
	}
	binary, argv := s.wrapCommand("opencode", []string{"run", "do-task"}, "/workspace", map[string]string{
		"GITHUB_TOKEN": "tok",
	})
	if binary != "docker" {
		t.Fatalf("expected binary=docker, got %q", binary)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"run", "--rm",
		"--network", "none",
		"--user", "nobody",
		"-v", "/workspace:/workspace",
		"-w", "/workspace",
		"--env", "GITHUB_TOKEN=tok",
		"ghcr.io/example/agent:latest",
		"opencode", "run", "do-task",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker argv missing %q; got: %s", want, joined)
		}
	}
}

func TestCliSandbox_DefaultsUserAndNetwork(t *testing.T) {
	s := &cliSandbox{image: "img:latest"}
	binary, argv := s.wrapCommand("claude", []string{}, "", nil)
	if binary != "docker" {
		t.Fatalf("expected docker")
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--user nobody") {
		t.Errorf("expected default user=nobody; got: %s", joined)
	}
	if !strings.Contains(joined, "--network none") {
		t.Errorf("expected default network=none; got: %s", joined)
	}
}

func TestCliRunner_Configure_Sandbox(t *testing.T) {
	r := &CliRunner{}
	err := r.Configure(map[string]any{
		"command": "opencode",
		"sandbox": map[string]any{
			"image":   "img:v1",
			"user":    "agent",
			"network": "egress-only",
		},
	})
	if err != nil {
		t.Fatalf("Configure error: %v", err)
	}
	if r.sandbox == nil {
		t.Fatal("expected sandbox to be set")
	}
	if r.sandbox.image != "img:v1" {
		t.Errorf("image: got %q", r.sandbox.image)
	}
	if r.sandbox.user != "agent" {
		t.Errorf("user: got %q", r.sandbox.user)
	}
	if r.sandbox.network != "egress-only" {
		t.Errorf("network: got %q", r.sandbox.network)
	}
}

func TestCliRunner_Configure_Sandbox_RequiresImage(t *testing.T) {
	r := &CliRunner{}
	err := r.Configure(map[string]any{
		"command": "opencode",
		"sandbox": map[string]any{
			"user": "nobody",
		},
	})
	if err == nil {
		t.Fatal("expected error when sandbox.image is missing")
	}
}

// ensure filteredHostEnv does not panic when overlay is nil and no env set
func TestFilteredHostEnv_NilOverlay(t *testing.T) {
	// Save and restore the real env
	origEnv := os.Environ()
	os.Clearenv()
	defer func() {
		os.Clearenv()
		for _, kv := range origEnv {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
	}()

	env := filteredHostEnv(nil)
	if env == nil {
		t.Error("expected non-nil slice even when env is empty")
	}
}
