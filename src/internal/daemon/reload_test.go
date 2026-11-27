package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
)

// reloadBaseConfig is a minimal config that passes Validate without the cli
// package's adapter/schema hooks (KnownAdapters and SourceConfigSchema are nil
// under `go test ./internal/daemon/`).
const reloadBaseConfig = `
version: "1"
runners:
  - id: claude
    type: claude
agents:
  - id: engineer
    runner: claude
    model: opus
workflows:
  - id: wf-a
    steps:
      - id: s1
        agent: engineer
        prompt: first
`

func writeReloadConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// newReloadTestDispatcher loads path the way `apiary run` does and returns a
// dispatcher carrying that config, with no DB, sources, or runners.
func newReloadTestDispatcher(t *testing.T, path string) *Dispatcher {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("base config does not validate: %v", errs)
	}
	return dispatcherWithConfig(cfg, &Dispatcher{configFile: path})
}

func TestReloadConfigAppliesWorkflowAgentAndSettingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)

	writeReloadConfig(t, path, `
version: "1"
runners:
  - id: claude
    type: claude
agents:
  - id: engineer
    runner: claude
    model: sonnet
settings:
  log_level: debug
workflows:
  - id: wf-a
    steps:
      - id: s1
        agent: engineer
        prompt: second
  - id: wf-b
    steps:
      - id: s1
        agent: engineer
        prompt: new
`)

	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("reload rejected: %s", res.Error)
	}
	if got := d.config().Workflows; len(got) != 2 {
		t.Fatalf("live config has %d workflows, want 2", len(got))
	}
	wf, ok := d.workflowByID("wf-a")
	if !ok || wf.Steps[0].Prompt != "second" {
		t.Fatalf("wf-a was not replaced by the reloaded definition: %+v", wf)
	}

	if strings.Join(res.Workflows.Added, ",") != "wf-b" {
		t.Errorf("workflows added = %v, want [wf-b]", res.Workflows.Added)
	}
	if strings.Join(res.Workflows.Changed, ",") != "wf-a" {
		t.Errorf("workflows changed = %v, want [wf-a]", res.Workflows.Changed)
	}
	if len(res.Workflows.Removed) != 0 {
		t.Errorf("workflows removed = %v, want none", res.Workflows.Removed)
	}
	if strings.Join(res.Agents.Changed, ",") != "engineer" {
		t.Errorf("agents changed = %v, want [engineer]", res.Agents.Changed)
	}
	if !containsString(res.SettingsChanged, "log_level") {
		t.Errorf("settings_changed = %v, want it to name log_level", res.SettingsChanged)
	}
	if len(res.RequiresRestart) != 0 {
		t.Errorf("requires_restart = %v, want none (log_level is live)", res.RequiresRestart)
	}

	// The result is remembered for /status.
	if last := d.lastReloadResult(); last == nil || !last.Applied {
		t.Fatalf("last reload not remembered: %+v", last)
	}
}

func TestReloadConfigRejectsInvalidConfigAndKeepsOld(t *testing.T) {
	cases := map[string]string{
		"invalid yaml": "version: \"1\"\nworkflows: [\n",
		"validation failure": `
runners:
  - id: claude
    type: claude
agents:
  - id: engineer
    runner: claude
    model: opus
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "apiary.yaml")
			writeReloadConfig(t, path, reloadBaseConfig)
			d := newReloadTestDispatcher(t, path)
			before := d.config()

			writeReloadConfig(t, path, content)
			res := d.ReloadConfig(context.Background())

			if res.Applied {
				t.Fatal("a config that does not load must not be applied")
			}
			if res.Error == "" {
				t.Fatal("the rejection must carry an error message")
			}
			if d.config() != before {
				t.Fatal("the old config must stay live, untouched, after a rejected reload")
			}
		})
	}
}

// A source whose adapter cannot be built rejects the whole reload: an added
// source that cannot connect must not leave the daemon half-configured. Full
// add/remove/edit coverage lives in source_reload_test.go.
func TestReloadConfigRejectsUnbuildableSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)
	before := d.config()

	writeReloadConfig(t, path, reloadBaseConfig+`
sources:
  - id: github
    type: github
`)

	res := d.ReloadConfig(context.Background())
	if res.Applied {
		t.Fatal("a source whose adapter cannot be built must be rejected")
	}
	if !strings.Contains(res.Error, `unknown type "github"`) {
		t.Errorf("error = %q, want it to name the unknown source type", res.Error)
	}
	if res.Sources.Added == nil || res.Sources.Added[0] != "github" {
		t.Errorf("sources added = %v, want [github]", res.Sources.Added)
	}
	if d.config() != before {
		t.Fatal("a rejected source change must leave the old config live")
	}
}

func TestReloadConfigAppliesStartupOnlyChangeWithRestartNotice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)

	writeReloadConfig(t, path, reloadBaseConfig+`
settings:
  log_max_age_days: 7
`)

	res := d.ReloadConfig(context.Background())
	if !res.Applied {
		t.Fatalf("a startup-only change is still applied: %s", res.Error)
	}
	if !containsString(res.RequiresRestart, "log_max_age_days") {
		t.Errorf("requires_restart = %v, want it to name log_max_age_days", res.RequiresRestart)
	}
	if got := d.config().Settings.LogMaxAgeDays; got != 7 {
		t.Errorf("the new value must be in the live config, got %d", got)
	}
}

func TestWatchConfigLoopReloadsOnChangeAndKeepsConfigOnBadWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)
	d.watchInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Take the baseline before starting the loop, then edit immediately: the
	// baseline would otherwise sometimes include the edit and miss it.
	baseline, ok := statConfigFile(path)
	if !ok {
		t.Fatal("stat baseline config")
	}
	go d.watchConfigFrom(ctx, baseline, true)

	// A change that validates is applied.
	writeReloadConfig(t, path, strings.Replace(reloadBaseConfig, "prompt: first", "prompt: watched", 1))
	waitFor(t, 5*time.Second, func() bool {
		wf, ok := d.workflowByID("wf-a")
		return ok && wf.Steps[0].Prompt == "watched"
	}, "watcher did not apply the changed workflow")

	applied := d.config()

	// A broken write is rejected once, and the good config stays live.
	writeReloadConfig(t, path, "version: \"1\"\nworkflows: [\n")
	waitFor(t, 5*time.Second, func() bool {
		last := d.lastReloadResult()
		return last != nil && !last.Applied && last.Error != ""
	}, "watcher did not report the rejected write")
	if d.config() != applied {
		t.Fatal("a rejected watched write must leave the running config in place")
	}
}

// TestStartLaunchesConfigWatcher covers the wiring: watch_config defaults to
// on, and the baseline is taken before Start returns, so an edit made right
// after it is still seen.
func TestStartLaunchesConfigWatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)
	d.watchInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	d.Start(ctx, &wg)
	defer func() {
		cancel()
		wg.Wait()
	}()

	writeReloadConfig(t, path, strings.Replace(reloadBaseConfig, "prompt: first", "prompt: from-start", 1))
	waitFor(t, 5*time.Second, func() bool {
		wf, ok := d.workflowByID("wf-a")
		return ok && wf.Steps[0].Prompt == "from-start"
	}, "Start did not launch a config watcher")
}

// TestReloadEndpointStatusCodes pins the IPC contract: 200 when applied, 409
// when rejected, and the body always carries the result.
func TestReloadEndpointStatusCodes(t *testing.T) {
	dir, err := os.MkdirTemp("", "apreload")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("APIARY_SOCKET", filepath.Join(dir, "s.sock"))

	path := filepath.Join(dir, "apiary.yaml")
	writeReloadConfig(t, path, reloadBaseConfig)
	d := newReloadTestDispatcher(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if err := d.StartServer(ctx, &wg); err != nil {
		t.Fatalf("start ipc server: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	post := func() (int, ReloadResult) {
		t.Helper()
		resp, err := unixHTTPClient(filepath.Join(dir, "s.sock")).Post("http://apiary/reload", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /reload: %v", err)
		}
		defer resp.Body.Close()
		var res ReloadResult
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode /reload body: %v", err)
		}
		return resp.StatusCode, res
	}

	// Rejected: source change.
	writeReloadConfig(t, path, reloadBaseConfig+`
sources:
  - id: github
    type: github
`)
	status, res := post()
	if status != http.StatusConflict {
		t.Errorf("rejected reload status = %d, want 409", status)
	}
	if res.Applied || res.Error == "" {
		t.Errorf("rejected reload body = %+v, want applied=false with an error", res)
	}

	// Applied: workflow change.
	writeReloadConfig(t, path, strings.Replace(reloadBaseConfig, "prompt: first", "prompt: applied", 1))
	status, res = post()
	if status != http.StatusOK {
		t.Errorf("applied reload status = %d, want 200", status)
	}
	if !res.Applied {
		t.Errorf("applied reload body = %+v, want applied=true", res)
	}
}

// unixHTTPClient talks to the daemon's control socket like the CLI does.
func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
