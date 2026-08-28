package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/plugin"
)

// installFixture publishes a real archive over TLS and a file:// index that
// describes it, so the tests exercise the same path an operator does.
type installFixture struct {
	registryURL string
	projectDir  string
	pluginDir   string
	client      *http.Client
}

func newInstallFixture(t *testing.T, versions ...string) *installFixture {
	t.Helper()
	if len(versions) == 0 {
		versions = []string{"1.0.0"}
	}
	project := t.TempDir()
	pluginDir := filepath.Join(project, ".apiary", "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(project, "apiary.yaml")
	if err := os.WriteFile(configPath, []byte("version: \"1.0\"\nplugin_dirs: [.apiary/plugins]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := configFile
	configFile = configPath
	t.Cleanup(func() { configFile = previous })

	archives := map[string][]byte{}
	var releases []plugin.Release
	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	for _, version := range versions {
		body := buildArchive(t, version)
		archives["/"+version] = body
		releases = append(releases, plugin.Release{
			Version: version, Apiary: ">= 0.0.1-0", Protocol: plugin.ProtocolVersion,
			Conformance: &plugin.Conformance{Status: "passed"},
			Artifacts: []plugin.Artifact{{
				OS: runtime.GOOS, Arch: runtime.GOARCH, URL: server.URL + "/" + version,
				ArchiveSHA256:    digestOf(body),
				ExecutableSHA256: digestOf([]byte(executableBody(version))),
			}},
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := archives[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})

	index := plugin.Index{SchemaVersion: 1, Plugins: []plugin.RegistryPlugin{{
		ID: "dev.apiary.example", Summary: "an example plugin",
		Capabilities: []plugin.Capability{plugin.CapabilitySource},
		Repository:   "https://example.invalid/repo", Releases: releases,
	}}}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(indexPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	// The installer builds its own client for artifact downloads, so the test
	// server's certificate has to be acceptable to the default transport.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	return &installFixture{registryURL: "file://" + indexPath, projectDir: project, pluginDir: pluginDir, client: server.Client()}
}

func executableBody(version string) string {
	return "#!/bin/sh\n# version " + version + "\nexit 0\n"
}

func buildArchive(t *testing.T, version string) []byte {
	t.Helper()
	manifest := fmt.Sprintf(`{"schema_version":1,"id":"dev.apiary.example","version":%q,"apiary":">= 0.0.1-0","protocol":1,"executable":"plugin.sh","capabilities":["source"],"config_schema":{"type":"object","properties":{"path":{"type":"string"},"depth":{"type":"integer"}},"required":["path","depth"],"additionalProperties":false},"security":{"network":true,"read_paths":["/etc/hosts"],"secret_env":["EXAMPLE_TOKEN"]}}`, version)

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	for _, entry := range []struct {
		name string
		body string
		mode int64
	}{
		{plugin.ManifestFilename, manifest, 0o644},
		{"plugin.sh", executableBody(version), 0o755},
	} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []func() error{writer.Close, gzipWriter.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Bytes()
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func runInstallCmd(t *testing.T, fixture *installFixture, args ...string) (string, error) {
	t.Helper()
	cmd := newPluginsInstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(args, "--registry", fixture.registryURL))
	err := cmd.Execute()
	return out.String(), err
}

func TestInstallVerifiesPinsAndDoesNotEnable(t *testing.T) {
	fixture := newInstallFixture(t)
	out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	root := filepath.Join(fixture.pluginDir, "dev.apiary.example")
	installed, err := plugin.Load(root, "")
	if err != nil {
		t.Fatalf("the installed plugin must validate: %v", err)
	}
	if installed.Manifest.Checksum == "" {
		t.Fatal("the installer must pin the executable it verified")
	}

	// The trust summary is not optional, and --yes suppresses the prompt only.
	for _, want := range []string{
		"Declared access", "network      yes", "secret env   EXAMPLE_TOKEN",
		"daemon's OS permissions", "not an", "endorsement",
		"(verified)", "from the registry, not from the archive",
		"Nothing runs yet", "plugins:", "id: dev.apiary.example",
		"path: \"\"", "depth: 0", // seeded from the manifest's required config keys
		"restart the daemon",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("install output is missing %q:\n%s", want, out)
		}
	}

	// Installing must never enable anything: apiary.yaml is left untouched.
	config, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "plugins:") {
		t.Fatalf("install wrote to apiary.yaml:\n%s", config)
	}
}

func TestInstallRefusesToReplaceAnInstalledPlugin(t *testing.T) {
	fixture := newInstallFixture(t)
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes")
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("want a refusal pointing at upgrade, got %v\n%s", err, out)
	}
}

// Declining at the prompt must leave the plugin directory exactly as it was.
func TestInstallAbortsOnDecline(t *testing.T) {
	fixture := newInstallFixture(t)
	cmd := newPluginsInstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"dev.apiary.example", "--registry", fixture.registryURL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing was installed") {
		t.Fatalf("want an explicit abort:\n%s", out.String())
	}
	entries, err := os.ReadDir(fixture.pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a declined install left files behind: %v", entries)
	}
}

func TestInstallRejectsAMismatchedExpectedDigest(t *testing.T) {
	fixture := newInstallFixture(t)
	out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes", "--sha256",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err == nil || !strings.Contains(err.Error(), "nothing was downloaded") {
		t.Fatalf("want a pre-download refusal, got %v\n%s", err, out)
	}
}

func TestUpgradeSwapsAndKeepsOneGeneration(t *testing.T) {
	fixture := newInstallFixture(t, "1.0.0", "1.1.0")
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example@1.0.0", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	cmd := newPluginsUpgradeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dev.apiary.example", "--yes", "--registry", fixture.registryURL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	installed, err := plugin.Load(filepath.Join(fixture.pluginDir, "dev.apiary.example"), "")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Manifest.Version != "1.1.0" {
		t.Fatalf("want 1.1.0 installed, got %s", installed.Manifest.Version)
	}
	kept, err := plugin.Load(filepath.Join(fixture.pluginDir, "dev.apiary.example.bak"), "")
	if err != nil {
		t.Fatalf("the previous version must be kept: %v", err)
	}
	if kept.Manifest.Version != "1.0.0" {
		t.Fatalf("want 1.0.0 kept, got %s", kept.Manifest.Version)
	}
	if !strings.Contains(out.String(), "daemon keeps the version it started with") {
		t.Fatalf("an upgrade must say the running daemon is unaffected:\n%s", out.String())
	}
}

func TestUpgradeRollbackRestoresTheKeptVersion(t *testing.T) {
	fixture := newInstallFixture(t, "1.0.0", "1.1.0")
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example@1.0.0", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	upgrade := newPluginsUpgradeCmd()
	upgrade.SetOut(&bytes.Buffer{})
	upgrade.SetArgs([]string{"dev.apiary.example", "--yes", "--registry", fixture.registryURL})
	if err := upgrade.Execute(); err != nil {
		t.Fatal(err)
	}

	rollback := newPluginsUpgradeCmd()
	var out bytes.Buffer
	rollback.SetOut(&out)
	rollback.SetErr(&out)
	rollback.SetArgs([]string{"dev.apiary.example", "--rollback"})
	if err := rollback.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	installed, err := plugin.Load(filepath.Join(fixture.pluginDir, "dev.apiary.example"), "")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Manifest.Version != "1.0.0" {
		t.Fatalf("rollback did not restore 1.0.0, got %s", installed.Manifest.Version)
	}
}

func TestUpgradeIsANoopWhenAlreadyNewest(t *testing.T) {
	fixture := newInstallFixture(t)
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cmd := newPluginsUpgradeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"dev.apiary.example", "--yes", "--registry", fixture.registryURL})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already the newest") {
		t.Fatalf("want a no-op message:\n%s", out.String())
	}
}

func TestUninstallRefusesWhileEnabled(t *testing.T) {
	fixture := newInstallFixture(t)
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	enabled := "version: \"1.0\"\nplugin_dirs: [.apiary/plugins]\nplugins:\n  - id: dev.apiary.example\n    config: {path: /tmp/x, depth: 1}\n"
	if err := os.WriteFile(configFile, []byte(enabled), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newPluginsUninstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dev.apiary.example"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "is enabled") {
		t.Fatalf("want a refusal while enabled, got %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.pluginDir, "dev.apiary.example")); err != nil {
		t.Fatal("the refused uninstall must not have removed anything")
	}

	forced := newPluginsUninstallCmd()
	out.Reset()
	forced.SetOut(&out)
	forced.SetErr(&out)
	forced.SetArgs([]string{"dev.apiary.example", "--force"})
	if err := forced.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.pluginDir, "dev.apiary.example")); !os.IsNotExist(err) {
		t.Fatal("--force must remove the plugin directory")
	}
	// Uninstall never edits config: the stale entry stays, for validate to flag.
	config, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "dev.apiary.example") {
		t.Fatal("uninstall must not edit apiary.yaml")
	}
}

func TestUninstallUnknownPlugin(t *testing.T) {
	newInstallFixture(t)
	cmd := newPluginsUninstallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dev.apiary.example"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("want a not-installed error, got %v", err)
	}
}

// The injected pin is only worth something if something checks it. The daemon
// does, per invocation; validate answers the same question up front.
func TestValidateCatchesATamperedExecutable(t *testing.T) {
	fixture := newInstallFixture(t)
	if out, err := runInstallCmd(t, fixture, "dev.apiary.example", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	executable := filepath.Join(fixture.pluginDir, "dev.apiary.example", "plugin.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nrm -rf /\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := newPluginsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "failed integrity check") {
		t.Fatalf("a swapped executable must be caught, got %v\n%s", err, out.String())
	}
}
