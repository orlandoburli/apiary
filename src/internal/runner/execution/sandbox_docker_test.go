package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireDocker skips unless a working docker daemon is reachable. Review of
// this PR correctly pointed out that argv-shape assertions alone are not
// evidence the sandbox works; these tests actually run containers.
func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("APIARY_SKIP_DOCKER_TESTS") != "" {
		t.Skip("APIARY_SKIP_DOCKER_TESTS set")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

// runSandboxed executes a shell command through the real wrapCommand argv.
func runSandboxed(t *testing.T, workDir string, envNames []string, script string) (string, error) {
	t.Helper()
	s := &cliSandbox{image: "busybox:latest"}
	bin, args := s.wrapCommand("sh", []string{"-c", script}, workDir, envNames)
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// HOME must be writable: with --user <uid> there is no /etc/passwd entry, so a
// stripped HOME resolves to "/" which is read-only, and every agent CLI writes a
// config dir on startup. This is the exact runtime break review found.
func TestSandbox_HomeIsWritable(t *testing.T) {
	requireDocker(t)
	out, err := runSandboxed(t, "", nil, `printf %s "$HOME" && mkdir -p "$HOME/.config/agent" && echo ok > "$HOME/.config/agent/x" && cat "$HOME/.config/agent/x"`)
	if err != nil {
		t.Fatalf("agent could not write to HOME inside the sandbox: %v\n%s", err, out)
	}
	if !strings.Contains(out, sandboxHome) {
		t.Errorf("expected HOME=%s inside container, got: %s", sandboxHome, out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected a successful write under HOME, got: %s", out)
	}
}

// The bind-mounted workspace must be writable by the container user, which is
// why --user defaults to the daemon's uid:gid rather than "nobody".
func TestSandbox_WorkspaceIsWritable(t *testing.T) {
	requireDocker(t)
	dir := t.TempDir()
	out, err := runSandboxed(t, dir, nil, `echo written > ./file.txt && cat ./file.txt`)
	if err != nil {
		t.Fatalf("agent could not write its workspace: %v\n%s", err, out)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "file.txt"))
	if readErr != nil || !strings.Contains(string(data), "written") {
		t.Errorf("workspace write did not land on the host: err=%v data=%q", readErr, string(data))
	}
}

// The rootfs must stay read-only outside the explicit tmpfs mounts.
func TestSandbox_RootfsIsReadOnly(t *testing.T) {
	requireDocker(t)
	out, _ := runSandboxed(t, "", nil, `echo x > /etc/passwd 2>&1 || echo BLOCKED`)
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected a write to /etc to be blocked by --read-only, got: %s", out)
	}
}

// Credentials cross by NAME only; the value must come from the docker client's
// own environment and never appear in argv.
func TestSandbox_CredentialForwardedByNameNotArgv(t *testing.T) {
	requireDocker(t)
	t.Setenv("APIARY_TEST_SECRET", "s3cr3t-value")
	s := &cliSandbox{image: "busybox:latest"}
	bin, args := s.wrapCommand("sh", []string{"-c", `printf %s "$APIARY_TEST_SECRET"`}, "", []string{"APIARY_TEST_SECRET"})
	for _, a := range args {
		if strings.Contains(a, "s3cr3t-value") {
			t.Fatalf("secret value leaked into argv: %q", a)
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "s3cr3t-value") {
		t.Errorf("credential did not reach the container: %q", string(out))
	}
}

// Host wiring must not cross the boundary — DOCKER_HOST especially, since it
// would hand an escaped agent the host Docker socket. Set real values on the
// host so the assertion is meaningful rather than tautological.
func TestSandbox_HostWiringNotForwarded(t *testing.T) {
	requireDocker(t)
	t.Setenv("SSH_AUTH_SOCK", "/host/ssh-agent.sock")
	out, err := runSandboxed(t, "", []string{"DOCKER_HOST", "SSH_AUTH_SOCK", "PATH"}, `printf "[%s]" "$SSH_AUTH_SOCK"`)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "/host/ssh-agent.sock") {
		t.Errorf("host SSH_AUTH_SOCK leaked into the container: %s", out)
	}
	if !strings.Contains(out, "[]") {
		t.Errorf("expected SSH_AUTH_SOCK to be empty inside the container, got: %s", out)
	}
}

// buildImageWithHome builds a throwaway image that PRE-CREATES the sandbox HOME
// as root:0755 — the case where a tmpfs inheriting the mountpoint mode leaves
// HOME unwritable for an unprivileged --user. busybox happens not to have the
// directory, which is why the earlier test passed against a broken sandbox.
func buildImageWithHome(t *testing.T) string {
	t.Helper()
	tag := "apiary-sandbox-hometest:latest"
	dockerfile := "FROM busybox:latest\nRUN mkdir -p " + sandboxHome + " && chmod 0755 " + sandboxHome + "\n"
	cmd := exec.Command("docker", "build", "-t", tag, "-")
	cmd.Stdin = strings.NewReader(dockerfile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build test image: %v\n%s", err, out)
	}
	return tag
}

// Regression: HOME must be writable even when the image already contains the
// directory owned by root with restrictive permissions.
func TestSandbox_HomeWritableWhenImagePreCreatesIt(t *testing.T) {
	requireDocker(t)
	tag := buildImageWithHome(t)
	s := &cliSandbox{image: tag}
	bin, args := s.wrapCommand("sh", []string{"-c", `touch "$HOME/probe" && echo WROTE`}, "", nil)
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "WROTE") {
		t.Fatalf("HOME must be writable even when the image pre-creates it as root:0755: err=%v\n%s", err, out)
	}
}

// Docker MERGES its tmpfs defaults (nodev,noexec,relatime), so "exec" has to be
// stated explicitly or agents cannot run npx/node native modules/git hooks.
func TestSandbox_TmpfsIsExecutable(t *testing.T) {
	requireDocker(t)
	script := `printf '#!/bin/sh\necho RAN\n' > "$HOME/s.sh" && chmod +x "$HOME/s.sh" && "$HOME/s.sh"`
	out, err := runSandboxed(t, "", nil, script)
	if err != nil || !strings.Contains(out, "RAN") {
		t.Fatalf("HOME tmpfs must allow execution (docker merges noexec by default): err=%v\n%s", err, out)
	}
	tmpScript := `printf '#!/bin/sh\necho TMPRAN\n' > /tmp/s.sh && chmod +x /tmp/s.sh && /tmp/s.sh`
	out, err = runSandboxed(t, "", nil, tmpScript)
	if err != nil || !strings.Contains(out, "TMPRAN") {
		t.Fatalf("/tmp tmpfs must allow execution: err=%v\n%s", err, out)
	}
}
