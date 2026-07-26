package execution

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func envMap(kvs []string) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestScopedEnv_RetainsProviderKeys_DropsUnrelatedSecrets(t *testing.T) {
	host := []string{
		"PATH=/usr/bin",
		"HOME=/home/apiary",
		"ANTHROPIC_API_KEY=sk-ant-xxx",  // provider family — must be kept (regression: PR #254 dropped it)
		"OPENAI_API_KEY=sk-oai-xxx",     // provider family — must be kept
		"AWS_SECRET_ACCESS_KEY=aws-xxx", // unrelated daemon secret — must be dropped
		"STRIPE_SECRET_KEY=sk-live-xxx", // unrelated daemon secret — must be dropped
	}
	env, names := scopedEnv(host, nil, nil)
	got := envMap(env)

	for _, keep := range []string{"PATH", "HOME", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if _, ok := got[keep]; !ok {
			t.Errorf("expected %q to be retained, but it was dropped", keep)
		}
	}
	for _, drop := range []string{"AWS_SECRET_ACCESS_KEY", "STRIPE_SECRET_KEY"} {
		if _, ok := got[drop]; ok {
			t.Errorf("expected %q to be dropped, but it was retained", drop)
		}
	}
	// names must mirror env and stay sorted/deduped.
	if len(names) != len(got) {
		t.Errorf("names (%d) and env (%d) length mismatch", len(names), len(got))
	}
}

func TestScopedEnv_OverlayWinsAndIsRetained(t *testing.T) {
	host := []string{"PATH=/usr/bin", "GITHUB_TOKEN=host-token"}
	// Per-task overlay must always be present and win over any host value.
	env, _ := scopedEnv(host, map[string]string{"GITHUB_TOKEN": "scoped-token"}, nil)
	got := envMap(env)
	if got["GITHUB_TOKEN"] != "scoped-token" {
		t.Errorf("overlay GITHUB_TOKEN not applied: got %q", got["GITHUB_TOKEN"])
	}
}

func TestScopedEnv_Passthrough(t *testing.T) {
	host := []string{"MYCORP_TOKEN=abc", "MYCORP_REGION=us", "OTHER_SECRET=zzz"}
	env, _ := scopedEnv(host, nil, []string{"MYCORP_*"})
	got := envMap(env)
	if _, ok := got["MYCORP_TOKEN"]; !ok {
		t.Error("MYCORP_TOKEN should pass through via prefix rule")
	}
	if _, ok := got["MYCORP_REGION"]; !ok {
		t.Error("MYCORP_REGION should pass through via prefix rule")
	}
	if _, ok := got["OTHER_SECRET"]; ok {
		t.Error("OTHER_SECRET must not pass through")
	}
}

func TestWrapCommand_NoSecretValuesInArgv(t *testing.T) {
	s := &cliSandbox{image: "apiary/agent:latest"}
	// Names only — values live in the docker process env, never in argv.
	_, args := s.wrapCommand("opencode", []string{"run", "--model", "x"}, "/work/task", []string{"GITHUB_TOKEN", "ANTHROPIC_API_KEY"})

	joined := strings.Join(args, " ")
	// The critical assertion: no "NAME=VALUE" env pair (i.e. no secret value) in argv.
	for _, a := range args {
		if strings.HasPrefix(a, "GITHUB_TOKEN=") || strings.HasPrefix(a, "ANTHROPIC_API_KEY=") {
			t.Fatalf("secret value leaked into argv: %q", a)
		}
	}
	// Name-only --env forwarding must be present.
	if !strings.Contains(joined, "--env GITHUB_TOKEN") || !strings.Contains(joined, "--env ANTHROPIC_API_KEY") {
		t.Errorf("expected name-only --env forwarding, got: %s", joined)
	}
	// Hardening flags + isolation: unprivileged user, read-only rootfs, no caps, workdir bind mount.
	for _, want := range []string{
		"run", "--rm",
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,mode=1777,size=" + tmpfsSize,
		"--cap-drop", "all",
		"--security-opt", "no-new-privileges",
		"-v", "/work/task:/work/task", "-w", "/work/task",
		"apiary/agent:latest", "opencode",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected docker args to contain %q, got: %s", want, joined)
		}
	}
}

func TestWrapCommand_NetworkDefaultsToBridge(t *testing.T) {
	s := &cliSandbox{image: "img"}
	_, args := s.wrapCommand("bin", nil, "", nil)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network bridge") {
		t.Errorf("network should default to bridge so agents keep egress, got: %s", joined)
	}
}

// DOCKER_HOST would hand the agent the host Docker socket — a trivial escape
// from the very sandbox this feature provides. Host paths/identity are likewise
// wrong inside the image (HOME under a read-only rootfs is the classic break).
func TestWrapCommand_DoesNotForwardHostWiringIntoContainer(t *testing.T) {
	names := []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN", "DOCKER_HOST", "SSH_AUTH_SOCK", "HOME", "PATH", "TMPDIR"}
	_, args := (&cliSandbox{image: "img"}).wrapCommand("bin", nil, "/w", names)
	joined := strings.Join(args, " ")

	// The host's values must not cross; note "--env HOME=<path>" (an explicit
	// container path we set) is different from "--env HOME" (forward host value).
	for _, leak := range []string{"DOCKER_HOST", "SSH_AUTH_SOCK", "PATH", "TMPDIR"} {
		if strings.Contains(joined, "--env "+leak+" ") || strings.HasSuffix(joined, "--env "+leak) {
			t.Errorf("%s must not be forwarded into the container: %s", leak, joined)
		}
	}
	if strings.Contains(joined, "--env HOME ") {
		t.Errorf("the host HOME value must not be forwarded: %s", joined)
	}
	// A writable HOME must be set explicitly, or agent CLIs cannot write config
	// under the read-only rootfs.
	if !strings.Contains(joined, "--env HOME="+sandboxHome) {
		t.Errorf("expected an explicit writable HOME, got: %s", joined)
	}
	for _, want := range []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"} {
		if !strings.Contains(joined, "--env "+want) {
			t.Errorf("credential %s should be forwarded, got: %s", want, joined)
		}
	}
}

// A bind-mounted workspace is owned by the daemon's uid; running as "nobody"
// makes every write fail with EACCES.
func TestWrapCommand_DefaultUserCanWriteWorkspace(t *testing.T) {
	_, args := (&cliSandbox{image: "img"}).wrapCommand("bin", nil, "/w", nil)
	want := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--user "+want) {
		t.Errorf("expected --user %s so workspace writes succeed, got: %s", want, joined)
	}
	if strings.Contains(joined, "--user nobody") {
		t.Error("must not default to nobody: bind-mounted workspace writes would EACCES")
	}
}

func TestValidateExtraArgs(t *testing.T) {
	// Every one of these defeats or weakens the sandbox. The last group is why an
	// allow-list replaced the original deny-list: pflag attached-shorthand and
	// boolean "=false" forms slipped straight past exact-match matching.
	for _, bad := range [][]string{
		{"--privileged"},
		{"--cap-add", "SYS_ADMIN"},
		{"--user=0:0"},
		{"-v", "/:/host"},
		{"--security-opt", "seccomp=unconfined"},
		{"--network=host"},
		{"--read-only=false"},       // silently disables the headline flag (last wins)
		{"--entrypoint", "/bin/sh"}, // replaces the agent binary
		{"--cgroupns=host"},
		{"--group-add", "docker"},
		{"--tmpfs", "/etc"}, // shadow /etc with a writable mount
		{"-v/:/hostfs"},     // pflag attached shorthand
		{"-u0:0"},           // pflag attached shorthand
		{"--sysctl", "net.ipv4.ip_forward=1"},
		{"/bin/sh"},  // bare value, not a flag
		{"--memory"}, // trailing flag with no value
	} {
		if err := validateExtraArgs(bad); err == nil {
			t.Errorf("expected %v to be rejected", bad)
		}
	}
	for _, ok := range [][]string{
		{"--memory", "2g"},
		{"--cpus=2"},
		{"--pids-limit", "256"},
		{"--ulimit", "nofile=1024:2048"},
		{"--label", "team=apiary", "--memory", "1g"},
	} {
		if err := validateExtraArgs(ok); err != nil {
			t.Errorf("benign args %v should be allowed, got %v", ok, err)
		}
	}
}
