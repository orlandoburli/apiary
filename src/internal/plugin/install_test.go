package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a real archive, serves it over TLS, and returns a Resolved
// pointing at it — the same shape resolution produces from a published index.
type fixture struct {
	resolved *Resolved
	server   *httptest.Server
	client   *http.Client
	archive  string
}

func newFixture(t *testing.T, options ...func(*fixtureOptions)) *fixture {
	t.Helper()
	settings := fixtureOptions{
		manifest:   `{"schema_version":1,"id":"dev.apiary.example","version":"1.0.0","apiary":">= 0.1.0-0","protocol":1,"executable":"plugin.sh","capabilities":["source"],"security":{"network":true,"read_paths":["/etc/hosts"],"secret_env":["EXAMPLE_TOKEN"]}}`,
		executable: "#!/bin/sh\nexit 0\n",
	}
	for _, option := range options {
		option(&settings)
	}
	archive := writeTarGz(t, []tarEntry{
		{name: ManifestFilename, body: settings.manifest},
		{name: "plugin.sh", body: settings.executable, mode: 0o755},
	})
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(server.Close)

	archiveDigest, err := FileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	unpacked := t.TempDir()
	root, err := ExtractArchive(archive, unpacked)
	if err != nil {
		t.Fatal(err)
	}
	executableDigest, err := FileSHA256(filepath.Join(root, "plugin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if settings.wrongArchiveDigest {
		archiveDigest = "sha256:" + testDigest
	}
	if settings.wrongExecutableDigest {
		executableDigest = "sha256:" + testDigest
	}

	artifact := Artifact{OS: "linux", Arch: "amd64", URL: server.URL + "/plugin.tar.gz",
		ArchiveSHA256: archiveDigest, ExecutableSHA256: executableDigest}
	release := Release{Version: settings.releaseVersion(), Apiary: ">= 0.1.0-0", Protocol: ProtocolVersion, Artifacts: []Artifact{artifact}}
	entry := &RegistryPlugin{ID: settings.entryID(), Summary: "example", Capabilities: []Capability{CapabilitySource},
		Repository: "https://example.invalid/repo", Releases: []Release{release}}

	return &fixture{
		resolved: &Resolved{Plugin: entry, Release: &entry.Releases[0], Artifact: &entry.Releases[0].Artifacts[0]},
		server:   server, client: server.Client(), archive: archive,
	}
}

type fixtureOptions struct {
	manifest              string
	executable            string
	entryIDOverride       string
	versionOverride       string
	wrongArchiveDigest    bool
	wrongExecutableDigest bool
}

func (o fixtureOptions) entryID() string {
	if o.entryIDOverride != "" {
		return o.entryIDOverride
	}
	return "dev.apiary.example"
}

func (o fixtureOptions) releaseVersion() string {
	if o.versionOverride != "" {
		return o.versionOverride
	}
	return "1.0.0"
}

func (f *fixture) stage(t *testing.T, opts StageOptions) (*Staged, error) {
	t.Helper()
	if opts.Client == nil {
		opts.Client = f.client
	}
	return Stage(context.Background(), f.resolved, "", opts)
}

func TestStageVerifiesAndPins(t *testing.T) {
	fixture := newFixture(t)
	staged, err := fixture.stage(t, StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Discard()

	if !staged.PinInjected {
		t.Fatal("an unpinned manifest must be pinned from the registry's digest")
	}
	if !DigestsMatch(staged.Installed.Manifest.Checksum, fixture.resolved.Artifact.ExecutableSHA256) {
		t.Fatalf("the injected pin must be the registry's digest, got %q", staged.Installed.Manifest.Checksum)
	}
	// The rewritten manifest has to be one the daemon accepts, from disk.
	reloaded, err := Load(staged.Root, "")
	if err != nil {
		t.Fatalf("the pinned manifest must still validate: %v", err)
	}
	if reloaded.Manifest.Checksum == "" {
		t.Fatal("the pin was not persisted")
	}
}

func TestStageKeepsAPublisherPin(t *testing.T) {
	// Build once to learn the digest, then republish with it pinned.
	probe := newFixture(t)
	staged, err := probe.stage(t, StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	digest := staged.Installed.Manifest.Checksum
	staged.Discard()

	pinned := newFixture(t, func(o *fixtureOptions) {
		o.manifest = strings.Replace(o.manifest, `"protocol":1,`, `"protocol":1,"checksum":"`+digest+`",`, 1)
	})
	staged, err = pinned.stage(t, StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Discard()
	if staged.PinInjected {
		t.Fatal("a publisher's pin must be left alone")
	}
}

func TestStageRejectsAPublisherPinThatIsNotTheShippedExecutable(t *testing.T) {
	fixture := newFixture(t, func(o *fixtureOptions) {
		o.manifest = strings.Replace(o.manifest, `"protocol":1,`, `"protocol":1,"checksum":"sha256:`+testDigest+`",`, 1)
	})
	_, err := fixture.stage(t, StageOptions{})
	if err == nil || !strings.Contains(err.Error(), "not the executable it ships") {
		t.Fatalf("want a pin/executable disagreement, got %v", err)
	}
}

func TestStageRejectsDigestMismatches(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		option func(*fixtureOptions)
		want   string
	}{
		{"archive", func(o *fixtureOptions) { o.wrongArchiveDigest = true }, "checksum mismatch for"},
		{"executable", func(o *fixtureOptions) { o.wrongExecutableDigest = true }, "checksum mismatch for the executable"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFixture(t, testCase.option)
			_, err := fixture.stage(t, StageOptions{})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("want %q, got %v", testCase.want, err)
			}
			if err != nil && !strings.Contains(err.Error(), "nothing was installed") {
				t.Fatalf("a mismatch must say nothing was installed, got %v", err)
			}
		})
	}
}

// The operator's --sha256 is a cross-check, not an override: a disagreement
// stops the install before a byte is fetched.
func TestStageRejectsExpectedDigestDisagreement(t *testing.T) {
	fixture := newFixture(t)
	_, err := fixture.stage(t, StageOptions{ExpectedArchiveSHA256: "sha256:" + testDigest})
	if err == nil || !strings.Contains(err.Error(), "nothing was downloaded") {
		t.Fatalf("want a pre-download refusal, got %v", err)
	}
}

func TestStageRejectsAnArchiveHoldingADifferentPlugin(t *testing.T) {
	fixture := newFixture(t, func(o *fixtureOptions) { o.entryIDOverride = "dev.apiary.other" })
	_, err := fixture.stage(t, StageOptions{})
	if err == nil || !strings.Contains(err.Error(), "was requested") {
		t.Fatalf("want an id disagreement, got %v", err)
	}
}

func TestStageRejectsAVersionTheRegistryDoesNotList(t *testing.T) {
	fixture := newFixture(t, func(o *fixtureOptions) { o.versionOverride = "2.0.0" })
	_, err := fixture.stage(t, StageOptions{})
	if err == nil || !strings.Contains(err.Error(), "the registry lists") {
		t.Fatalf("want a version disagreement, got %v", err)
	}
}

func TestStageLeavesNothingBehindOnFailure(t *testing.T) {
	fixture := newFixture(t, func(o *fixtureOptions) { o.wrongArchiveDigest = true })
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "apiary-install-*"))
	if _, err := fixture.stage(t, StageOptions{}); err == nil {
		t.Fatal("expected a failure")
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "apiary-install-*"))
	if len(after) > len(before) {
		t.Fatalf("a failed stage left staging directories behind: %v", after)
	}
}

func TestCommitIsAtomicAndDiscoverable(t *testing.T) {
	fixture := newFixture(t)
	staged, err := fixture.stage(t, StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := t.TempDir()
	destination := filepath.Join(pluginDir, "dev.apiary.example")
	if err := staged.Commit(destination); err != nil {
		t.Fatal(err)
	}
	registry, errs := Discover([]string{pluginDir}, pluginDir, "")
	if len(errs) > 0 {
		t.Fatalf("the committed plugin must discover cleanly: %v", errs)
	}
	if _, ok := registry.Get("dev.apiary.example"); !ok {
		t.Fatal("the committed plugin was not discovered")
	}
}

// The cross-device path copies rather than renames; the manifest is written
// last so a half-copy is never discoverable.
func TestCopyPluginDirWritesTheManifestLast(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ManifestFilename), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A copy that fails on the payload must not have written a manifest, since
	// a directory carrying one is visible to discovery.
	if err := os.WriteFile(filepath.Join(target, "plugin.sh"), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyPluginDir(source, target); err == nil {
		t.Fatal("expected the payload copy to fail")
	}
	if _, err := os.Stat(filepath.Join(target, ManifestFilename)); err == nil {
		t.Fatal("a failed copy must not leave a discoverable manifest behind")
	}
}

func TestSafeArchiveNameIgnoresPathsInURLs(t *testing.T) {
	for _, url := range []string{"https://example.invalid/../../etc/passwd", "https://example.invalid/"} {
		if name := safeArchiveName(url); strings.Contains(name, "..") || strings.ContainsRune(name, filepath.Separator) {
			t.Fatalf("unsafe archive name %q from %q", name, url)
		}
	}
}
