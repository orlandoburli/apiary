package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testArtifact(goos, goarch string) Artifact {
	return Artifact{OS: goos, Arch: goarch, URL: "https://example.invalid/p.tar.gz", ArchiveSHA256: testDigest, ExecutableSHA256: testDigest}
}

func testEntry(releases ...Release) *RegistryPlugin {
	return &RegistryPlugin{
		ID: "dev.apiary.example", Summary: "example", Capabilities: []Capability{CapabilitySource},
		Repository: "https://example.invalid/repo", Releases: releases,
	}
}

func testRelease(version, constraint string) Release {
	return Release{Version: version, Apiary: constraint, Protocol: ProtocolVersion, Artifacts: []Artifact{testArtifact("linux", "amd64")}}
}

func TestParseIndexRejectsUnknownSchemaVersion(t *testing.T) {
	_, err := ParseIndex([]byte(`{"schema_version": 99, "plugins": []}`))
	if err == nil || !strings.Contains(err.Error(), "schema_version 99") {
		t.Fatalf("want a fail-closed schema version error, got %v", err)
	}
}

func TestParseIndexRejectsUnknownFields(t *testing.T) {
	_, err := ParseIndex([]byte(`{"schema_version": 1, "plugins": [], "surprise": true}`))
	if err == nil {
		t.Fatal("an index with unknown fields must be rejected, not partially interpreted")
	}
}

func TestParseIndexRejectsDuplicateIDs(t *testing.T) {
	index := Index{SchemaVersion: 1, Plugins: []RegistryPlugin{*testEntry(testRelease("1.0.0", ">= 0.1.0-0")), *testEntry(testRelease("1.0.0", ">= 0.1.0-0"))}}
	encoded, _ := json.Marshal(index)
	if _, err := ParseIndex(encoded); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a duplicate-id rejection, got %v", err)
	}
}

func TestParseIndexRejectsUnpinnedArtifact(t *testing.T) {
	release := testRelease("1.0.0", ">= 0.1.0-0")
	release.Artifacts[0].ExecutableSHA256 = ""
	index := Index{SchemaVersion: 1, Plugins: []RegistryPlugin{*testEntry(release)}}
	encoded, _ := json.Marshal(index)
	if _, err := ParseIndex(encoded); err == nil || !strings.Contains(err.Error(), "executable_sha256") {
		t.Fatalf("an artifact without both digests must be rejected, got %v", err)
	}
}

func TestValidateRequiresYankReason(t *testing.T) {
	release := testRelease("1.0.0", ">= 0.1.0-0")
	release.Yanked = true
	errs := testEntry(release).Validate()
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "yanked_reason") {
		t.Fatalf("a yank must state why, got %v", errs)
	}
}

func TestResolvePicksNewestUsableRelease(t *testing.T) {
	entry := testEntry(
		testRelease("1.0.0", ">= 0.1.0-0"),
		testRelease("1.2.0", ">= 0.1.0-0"),
		testRelease("1.1.0", ">= 0.1.0-0"),
	)
	resolved, err := entry.Resolve(ResolveOptions{ApiaryVersion: "0.19.0", GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Release.Version != "1.2.0" {
		t.Fatalf("want 1.2.0, got %s", resolved.Release.Version)
	}
}

func TestResolveSkipsYankedAndReportsWhy(t *testing.T) {
	newest := testRelease("2.0.0", ">= 0.1.0-0")
	newest.Yanked = true
	newest.YankedReason = "corrupt archive"
	entry := testEntry(testRelease("1.0.0", ">= 0.1.0-0"), newest)

	resolved, err := entry.Resolve(ResolveOptions{ApiaryVersion: "0.19.0", GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Release.Version != "1.0.0" {
		t.Fatalf("a yanked release must not be selected, got %s", resolved.Release.Version)
	}
}

// Each rejection has to name the predicate that eliminated the newest
// candidate — that answer is the reason resolution happens before a download.
func TestResolveExplainsEachRejection(t *testing.T) {
	cases := []struct {
		name    string
		entry   *RegistryPlugin
		options ResolveOptions
		want    string
	}{
		{
			name:    "host version too old",
			entry:   testEntry(testRelease("1.0.0", ">= 0.20.0-0")),
			options: ResolveOptions{ApiaryVersion: "0.19.1", GOOS: "linux", GOARCH: "amd64"},
			want:    "requires apiary >= 0.20.0-0, this host is 0.19.1",
		},
		{
			name: "no build for this platform",
			entry: testEntry(Release{Version: "1.0.0", Apiary: ">= 0.1.0-0", Protocol: ProtocolVersion,
				Artifacts: []Artifact{testArtifact("linux", "amd64")}}),
			options: ResolveOptions{ApiaryVersion: "0.19.1", GOOS: "windows", GOARCH: "arm64"},
			want:    "has no build for windows/arm64",
		},
		{
			name: "future protocol",
			entry: testEntry(Release{Version: "1.0.0", Apiary: ">= 0.1.0-0", Protocol: ProtocolVersion + 1,
				Artifacts: []Artifact{testArtifact("linux", "amd64")}}),
			options: ResolveOptions{ApiaryVersion: "0.19.1", GOOS: "linux", GOARCH: "amd64"},
			want:    "speaks plugin protocol",
		},
		{
			name: "only a yanked release",
			entry: func() *RegistryPlugin {
				release := testRelease("1.0.0", ">= 0.1.0-0")
				release.Yanked, release.YankedReason = true, "corrupt archive"
				return testEntry(release)
			}(),
			options: ResolveOptions{ApiaryVersion: "0.19.1", GOOS: "linux", GOARCH: "amd64"},
			want:    "withdrawn by its publisher (corrupt archive)",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.entry.Resolve(testCase.options)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("want an error mentioning %q, got %v", testCase.want, err)
			}
		})
	}
}

// A development build's version string is whatever git describe invented.
// Refusing every plugin on that basis would be a worse answer than resolving and
// letting the daemon's own manifest check decide.
func TestResolveToleratesNonSemverHostVersion(t *testing.T) {
	entry := testEntry(testRelease("1.0.0", ">= 0.20.0-0"))
	resolved, err := entry.Resolve(ResolveOptions{ApiaryVersion: "sdk/v1.0.1-1-gabcdef-dirty", GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.CompatibilityUnchecked == "" {
		t.Fatal("skipping the constraint must be reported, not silent")
	}
}

func TestResolveExactVersion(t *testing.T) {
	entry := testEntry(testRelease("1.0.0", ">= 0.1.0-0"), testRelease("2.0.0", ">= 0.1.0-0"))
	resolved, err := entry.Resolve(ResolveOptions{Version: "1.0.0", ApiaryVersion: "0.19.0", GOOS: "linux", GOARCH: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Release.Version != "1.0.0" {
		t.Fatalf("want the requested 1.0.0, got %s", resolved.Release.Version)
	}
	if _, err := entry.Resolve(ResolveOptions{Version: "9.9.9", ApiaryVersion: "0.19.0"}); err == nil {
		t.Fatal("an unknown version must be an error")
	}
}

func TestSearchAndSuggest(t *testing.T) {
	index := &Index{SchemaVersion: 1, Plugins: []RegistryPlugin{
		{ID: "dev.apiary.routines", Summary: "scheduled routines", Capabilities: []Capability{CapabilitySource}},
		{ID: "dev.apiary.exporter", Summary: "event exporter", Capabilities: []Capability{CapabilityEventExporter}},
	}}
	if got := index.Search("routine", ""); len(got) != 1 || got[0].ID != "dev.apiary.routines" {
		t.Fatalf("query match failed: %v", got)
	}
	if got := index.Search("", CapabilityEventExporter); len(got) != 1 || got[0].ID != "dev.apiary.exporter" {
		t.Fatalf("capability filter failed: %v", got)
	}
	if got := index.Search("", ""); len(got) != 2 {
		t.Fatalf("an empty query lists everything, got %v", got)
	}
	if got := index.Suggest("dev.apiary.routine"); len(got) != 1 || got[0] != "dev.apiary.routines" {
		t.Fatalf("a typo should suggest the real id, got %v", got)
	}
}
