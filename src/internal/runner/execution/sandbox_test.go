package execution

import (
	"strings"
	"testing"
)

// TestFilteredHostEnv_AllowlistedKeysPass verifies that keys in hostEnvAllowList
// are forwarded and unrecognised keys are dropped.
func TestFilteredHostEnv_AllowlistedKeysPass(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "other-secret")

	env := filteredHostEnv(nil)

	got := make(map[string]string)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}

	if got["PATH"] != "/usr/bin" {
		t.Errorf("PATH should pass through; got %q", got["PATH"])
	}
	if got["HOME"] != "/home/test" {
		t.Errorf("HOME should pass through; got %q", got["HOME"])
	}
	if _, found := got["ANTHROPIC_API_KEY"]; found {
		t.Error("ANTHROPIC_API_KEY must not pass through the filter")
	}
	if _, found := got["AWS_SECRET_ACCESS_KEY"]; found {
		t.Error("AWS_SECRET_ACCESS_KEY must not pass through the filter")
	}
}

// TestFilteredHostEnv_OverlayAdded verifies that overlay keys appear in the
// output even when they are not in the allowlist.
func TestFilteredHostEnv_OverlayAdded(t *testing.T) {
	overlay := map[string]string{"CUSTOM_TOKEN": "tok123"}
	env := filteredHostEnv(overlay)

	got := make(map[string]string)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}

	if got["CUSTOM_TOKEN"] != "tok123" {
		t.Errorf("overlay key CUSTOM_TOKEN should appear; got %q", got["CUSTOM_TOKEN"])
	}
}

// TestFilteredHostEnv_OverlayPrecedence verifies that when an overlay key
// collides with a host allowlist key the overlay value is the one present
// (both entries appear because we append; the last one wins in exec, but
// we verify the overlay value is in the slice).
func TestFilteredHostEnv_OverlayPrecedence(t *testing.T) {
	t.Setenv("HOME", "/home/daemon")
	overlay := map[string]string{"HOME": "/home/task"}
	env := filteredHostEnv(overlay)

	// Find the last occurrence of HOME — that's what exec uses.
	last := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			last = kv
		}
	}
	if last != "HOME=/home/task" {
		t.Errorf("overlay HOME should win; last entry was %q", last)
	}
}

// TestWrapCommand_NoSecretsInArgv verifies that credentials in the overlay do
// NOT appear as --env KEY=VALUE in the docker argv (which would expose them via
// /proc/<pid>/cmdline). Instead only --env KEY is emitted, with the value
// delivered through the returned hostEnv.
func TestWrapCommand_NoSecretsInArgv(t *testing.T) {
	s := &cliSandbox{image: "myimage:latest", user: "nobody", network: "none"}
	overlay := map[string]string{"ANTHROPIC_API_KEY": "sk-secret"}

	_, dockerArgv, hostEnv := s.wrapCommand("claude", []string{"--model", "claude-3"}, "/work", overlay)

	// argv must contain --env ANTHROPIC_API_KEY but NOT --env ANTHROPIC_API_KEY=sk-secret
	for i, arg := range dockerArgv {
		if arg == "--env" && i+1 < len(dockerArgv) {
			val := dockerArgv[i+1]
			if strings.Contains(val, "=") && strings.HasPrefix(val, "ANTHROPIC_API_KEY") {
				t.Errorf("secret must not appear as KEY=VALUE in argv; found %q", val)
			}
		}
	}

	// The secret must be present in the returned hostEnv so docker can inject it.
	found := false
	for _, kv := range hostEnv {
		if kv == "ANTHROPIC_API_KEY=sk-secret" {
			found = true
		}
	}
	if !found {
		t.Error("ANTHROPIC_API_KEY=sk-secret must be in hostEnv so docker resolves --env KEY")
	}
}

// TestWrapCommand_SecurityFlags verifies that the hardened docker flags are
// always present regardless of sandbox config.
func TestWrapCommand_SecurityFlags(t *testing.T) {
	s := &cliSandbox{image: "myimage:latest"}
	dockerBin, dockerArgv, _ := s.wrapCommand("opencode", []string{"run", "task"}, "/repo", nil)

	if dockerBin != "docker" {
		t.Errorf("expected binary=docker; got %q", dockerBin)
	}

	required := []string{"--read-only", "--cap-drop", "all", "--security-opt", "no-new-privileges"}
	joined := strings.Join(dockerArgv, " ")
	for _, flag := range required {
		if !strings.Contains(joined, flag) {
			t.Errorf("expected docker arg %q in argv %v", flag, dockerArgv)
		}
	}
}

// TestWrapCommand_DefaultUserAndNetwork verifies defaults.
func TestWrapCommand_DefaultUserAndNetwork(t *testing.T) {
	s := &cliSandbox{image: "myimage:latest"} // user and network left empty
	_, dockerArgv, _ := s.wrapCommand("claude", nil, "", nil)

	joined := strings.Join(dockerArgv, " ")
	if !strings.Contains(joined, "--user nobody") {
		t.Errorf("expected --user nobody in %v", dockerArgv)
	}
	if !strings.Contains(joined, "--network none") {
		t.Errorf("expected --network none in %v", dockerArgv)
	}
}

// TestWrapCommand_WorkdirMount verifies the working directory is bind-mounted.
func TestWrapCommand_WorkdirMount(t *testing.T) {
	s := &cliSandbox{image: "myimage:latest"}
	_, dockerArgv, _ := s.wrapCommand("claude", nil, "/repo/workspace", nil)

	joined := strings.Join(dockerArgv, " ")
	if !strings.Contains(joined, "-v /repo/workspace:/repo/workspace") {
		t.Errorf("expected workdir bind mount in argv %v", dockerArgv)
	}
	if !strings.Contains(joined, "-w /repo/workspace") {
		t.Errorf("expected -w /repo/workspace in argv %v", dockerArgv)
	}
}

// TestWrapCommand_ImageAndBinaryLast verifies image and binary appear at the
// end (after all docker flags).
func TestWrapCommand_ImageAndBinaryLast(t *testing.T) {
	s := &cliSandbox{image: "myimage:latest"}
	_, dockerArgv, _ := s.wrapCommand("claude", []string{"-p", "hello"}, "", nil)

	// image should come before binary which comes before forwarded args
	imageIdx, binaryIdx, promptIdx := -1, -1, -1
	for i, a := range dockerArgv {
		switch a {
		case "myimage:latest":
			imageIdx = i
		case "claude":
			binaryIdx = i
		case "hello":
			promptIdx = i
		}
	}
	if imageIdx < 0 || binaryIdx < 0 || promptIdx < 0 {
		t.Fatalf("image=%d binary=%d prompt=%d; argv=%v", imageIdx, binaryIdx, promptIdx, dockerArgv)
	}
	if !(imageIdx < binaryIdx && binaryIdx < promptIdx) {
		t.Errorf("expected image < binary < args; positions image=%d binary=%d prompt=%d", imageIdx, binaryIdx, promptIdx)
	}
}

// TestConfigureSandbox_RequiresImage verifies that Configure rejects a sandbox
// block with no image.
func TestConfigureSandbox_RequiresImage(t *testing.T) {
	r := &CliRunner{}
	err := r.Configure(map[string]any{
		"command": "claude",
		"sandbox": map[string]any{
			"user": "nobody",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox.image") {
		t.Errorf("expected error about sandbox.image; got %v", err)
	}
}

// TestConfigureSandbox_ParsesFields verifies that Configure reads all sandbox fields.
func TestConfigureSandbox_ParsesFields(t *testing.T) {
	r := &CliRunner{}
	err := r.Configure(map[string]any{
		"command": "opencode",
		"sandbox": map[string]any{
			"image":      "myimage:v1",
			"user":       "1000:1000",
			"network":    "bridge",
			"extra_args": []any{"-v", "/cache:/cache:ro"},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if r.sandbox == nil {
		t.Fatal("expected sandbox to be set")
	}
	if r.sandbox.image != "myimage:v1" {
		t.Errorf("image: want myimage:v1 got %q", r.sandbox.image)
	}
	if r.sandbox.user != "1000:1000" {
		t.Errorf("user: want 1000:1000 got %q", r.sandbox.user)
	}
	if r.sandbox.network != "bridge" {
		t.Errorf("network: want bridge got %q", r.sandbox.network)
	}
	if len(r.sandbox.extraArgs) != 2 || r.sandbox.extraArgs[0] != "-v" {
		t.Errorf("extra_args: want [\"-v\",\"/cache:/cache:ro\"] got %v", r.sandbox.extraArgs)
	}
}
