package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/kardianos/service"
)

func testInstallOptions() serviceInstallOptions {
	return serviceInstallOptions{
		Name:        "apiary",
		ConfigPath:  "/home/u/proj/apiary.yaml",
		WorkDir:     "/home/u/proj",
		PathEnv:     "/home/u/.local/bin:/opt/homebrew/bin:/usr/bin",
		LogDir:      "/home/u/proj/.apiary/logs",
		StopTimeout: 10 * time.Minute,
	}
}

func TestBuildServiceConfig_RunsDaemonAsUser(t *testing.T) {
	cfg := buildServiceConfig(testInstallOptions(), "darwin")

	wantArgs := []string{"run", "--config", "/home/u/proj/apiary.yaml"}
	if !reflect.DeepEqual(cfg.Arguments, wantArgs) {
		t.Errorf("Arguments = %v, want %v", cfg.Arguments, wantArgs)
	}
	if cfg.WorkingDirectory != "/home/u/proj" {
		t.Errorf("WorkingDirectory = %q", cfg.WorkingDirectory)
	}
	if cfg.EnvVars["PATH"] != "/home/u/.local/bin:/opt/homebrew/bin:/usr/bin" {
		t.Errorf("PATH = %q", cfg.EnvVars["PATH"])
	}
	if v, _ := cfg.Option["UserService"].(bool); !v {
		t.Error("UserService must be true: the daemon must never be installed as a root system service")
	}
	if cfg.UserName != "" {
		t.Errorf("UserName = %q, want empty (user services run as the installing user)", cfg.UserName)
	}
	if cfg.Name == legacyServiceName {
		t.Errorf("service name must differ from the legacy %q", legacyServiceName)
	}
}

func TestBuildServiceConfig_OptionalArguments(t *testing.T) {
	o := testInstallOptions()
	o.EnvFile = "/home/u/proj/.env.prod"
	o.Profile = "cheap"
	o.Executable = "/usr/local/bin/apiary"
	cfg := buildServiceConfig(o, "linux")

	want := []string{"run", "--config", "/home/u/proj/apiary.yaml", "--env-file", "/home/u/proj/.env.prod", "--profile", "cheap"}
	if !reflect.DeepEqual(cfg.Arguments, want) {
		t.Errorf("Arguments = %v, want %v", cfg.Arguments, want)
	}
	if cfg.Executable != "/usr/local/bin/apiary" {
		t.Errorf("Executable = %q", cfg.Executable)
	}
}

func TestBuildServiceConfig_SystemdEscapesPath(t *testing.T) {
	o := testInstallOptions()
	o.PathEnv = `/opt/100%/bin:/opt/my "tools"/bin`
	cfg := buildServiceConfig(o, "linux")
	if got, want := cfg.EnvVars["PATH"], `/opt/100%%/bin:/opt/my \"tools\"/bin`; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	// launchd values are XML-escaped by the template, not here.
	if got := buildServiceConfig(o, "darwin").EnvVars["PATH"]; got != o.PathEnv {
		t.Errorf("darwin PATH = %q, want it untouched", got)
	}
}

// renderServiceTemplate executes a definition template the way kardianos does
// at install time. The funcs mirror the names kardianos registers.
func renderServiceTemplate(t *testing.T, cfg *service.Config, optionKey, path string) string {
	t.Helper()
	ident := func(s string) string { return s }
	funcs := template.FuncMap{"cmd": ident, "cmdEscape": ident}
	tpl, err := template.New("").Funcs(funcs).Parse(cfg.Option[optionKey].(string))
	if err != nil {
		t.Fatalf("parse %s: %v", optionKey, err)
	}
	data := struct {
		*service.Config
		Path              string
		StandardOutPath   string
		StandardErrorPath string
	}{cfg, path, "/logs/apiary.out.log", "/logs/apiary.err.log"}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute %s: %v", optionKey, err)
	}
	return buf.String()
}

func TestLaunchdDefinition(t *testing.T) {
	cfg := buildServiceConfig(testInstallOptions(), "darwin")
	out := renderServiceTemplate(t, cfg, "LaunchdConfig", "/usr/local/bin/apiary")

	for _, want := range []string{
		"<string>/usr/local/bin/apiary</string>\n\t\t<string>run</string>\n\t\t<string>--config</string>\n\t\t<string>/home/u/proj/apiary.yaml</string>",
		"<key>WorkingDirectory</key>\n\t<string>/home/u/proj</string>",
		"<key>PATH</key>\n\t\t<string>/home/u/.local/bin:/opt/homebrew/bin:/usr/bin</string>",
		// Restart on crash only: a clean SIGTERM exit must stay stopped.
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>ExitTimeOut</key>\n\t<integer>600</integer>",
		"<key>ThrottleInterval</key>\n\t<integer>30</integer>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "UserName") {
		t.Error("a LaunchAgent must not set UserName")
	}

	if runtime.GOOS == "darwin" {
		if plutil, err := exec.LookPath("plutil"); err == nil {
			f := filepath.Join(t.TempDir(), "apiary.plist")
			if err := os.WriteFile(f, []byte(out), 0o600); err != nil {
				t.Fatal(err)
			}
			if b, err := exec.Command(plutil, "-lint", f).CombinedOutput(); err != nil {
				t.Errorf("plutil -lint: %v\n%s", err, b)
			}
		}
	}
}

func TestSystemdDefinition(t *testing.T) {
	cfg := buildServiceConfig(testInstallOptions(), "linux")
	out := renderServiceTemplate(t, cfg, "SystemdScript", "/usr/local/bin/apiary")

	for _, want := range []string{
		"ExecStart=/usr/local/bin/apiary run --config /home/u/proj/apiary.yaml\n",
		"WorkingDirectory=/home/u/proj\n",
		`Environment="PATH=/home/u/.local/bin:/opt/homebrew/bin:/usr/bin"` + "\n",
		"Restart=on-failure\n",
		"KillMode=mixed\n",
		"TimeoutStopSec=600\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q\n---\n%s", want, out)
		}
	}
	for _, bad := range []string{"User=", "multi-user.target", "Restart=always"} {
		if strings.Contains(out, bad) {
			t.Errorf("user unit must not contain %q\n---\n%s", bad, out)
		}
	}
}

func TestServiceSupported(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		if err := serviceSupported(goos); err != nil {
			t.Errorf("%s: %v", goos, err)
		}
	}
	if err := serviceSupported("windows"); err == nil {
		t.Error("windows must be rejected: `apiary run` does not speak the SCM protocol")
	}
}

func TestLegacyServiceWarning(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }

	if msg := legacyServiceWarning("darwin", no); msg != "" {
		t.Errorf("no legacy file, got warning %q", msg)
	}
	mac := legacyServiceWarning("darwin", yes)
	for _, want := range []string{"/Library/LaunchDaemons/apiary-dispatcher.plist", "sudo launchctl bootout system/apiary-dispatcher", "sudo rm "} {
		if !strings.Contains(mac, want) {
			t.Errorf("darwin warning missing %q:\n%s", want, mac)
		}
	}
	linux := legacyServiceWarning("linux", yes)
	for _, want := range []string{"/etc/systemd/system/apiary-dispatcher.service", "sudo systemctl disable --now apiary-dispatcher"} {
		if !strings.Contains(linux, want) {
			t.Errorf("linux warning missing %q:\n%s", want, linux)
		}
	}
	if msg := legacyServiceWarning("windows", yes); msg != "" {
		t.Errorf("windows has no legacy path, got %q", msg)
	}
}

func TestParseLaunchctlPID(t *testing.T) {
	running := "{\n\t\"Label\" = \"apiary\";\n\t\"PID\" = 4242;\n};"
	if got := parseLaunchctlPID(running); got != 4242 {
		t.Errorf("pid = %d, want 4242", got)
	}
	if got := parseLaunchctlPID("{\n\t\"Label\" = \"apiary\";\n};"); got != 0 {
		t.Errorf("pid = %d, want 0 for a loaded-but-not-running job", got)
	}
}

func TestWaitForExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal 0 probing is unix-only")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	notified := 0
	err := waitForExit(pid, 600*time.Millisecond, func() { notified++ })
	if err == nil || !strings.Contains(err.Error(), "still draining") {
		t.Errorf("live process: err = %v, want a still-draining timeout", err)
	}
	if notified != 1 {
		t.Errorf("notify fired %d times, want once", notified)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	notified = 0
	if err := waitForExit(pid, 5*time.Second, func() { notified++ }); err != nil {
		t.Errorf("exited process: %v", err)
	}
	if notified != 0 {
		t.Error("notify must not fire for a process that is already gone")
	}
}

func TestDedupePathList(t *testing.T) {
	sep := string(os.PathListSeparator)
	in := strings.Join([]string{"/a", "/b", "/a", "", "/c", "/b"}, sep)
	if got, want := dedupePathList(in), strings.Join([]string{"/a", "/b", "/c"}, sep); got != want {
		t.Errorf("dedupePathList = %q, want %q", got, want)
	}
}
