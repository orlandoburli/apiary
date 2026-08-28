package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validEntryYAML = `schema_version: 1
id: dev.apiary.example
summary: an example
capabilities: [source]
repository: https://example.invalid/repo
releases:
  - version: 1.0.0
    apiary: ">= 0.1.0-0"
    protocol: 1
    artifacts:
      - os: linux
        arch: amd64
        url: https://example.invalid/p.tar.gz
        archive_sha256: ` + testDigest + `
        executable_sha256: ` + testDigest + `
`

func writeEntry(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadEntryAcceptsAValidListing(t *testing.T) {
	path := writeEntry(t, t.TempDir(), "dev.apiary.example.yaml", validEntryYAML)
	entry, err := LoadEntry(path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "dev.apiary.example" || len(entry.Releases) != 1 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

// A listing reviewed under one name must not publish under another.
func TestLoadEntryRequiresFilenameToMatchID(t *testing.T) {
	path := writeEntry(t, t.TempDir(), "something-else.yaml", validEntryYAML)
	if _, err := LoadEntry(path); err == nil || !strings.Contains(err.Error(), "filename") {
		t.Fatalf("want a filename/id mismatch error, got %v", err)
	}
}

func TestLoadEntryRejectsUnknownFields(t *testing.T) {
	path := writeEntry(t, t.TempDir(), "dev.apiary.example.yaml", validEntryYAML+"surprise: true\n")
	if _, err := LoadEntry(path); err == nil {
		t.Fatal("an entry with unknown fields must be rejected")
	}
}

func TestLoadEntryRejectsUnknownSchemaVersion(t *testing.T) {
	body := strings.Replace(validEntryYAML, "schema_version: 1", "schema_version: 7", 1)
	path := writeEntry(t, t.TempDir(), "dev.apiary.example.yaml", body)
	if _, err := LoadEntry(path); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("want a fail-closed schema version error, got %v", err)
	}
}

// A registry that partially parses would publish exactly the listings nobody
// checked.
func TestLoadEntriesFailsOnAnyInvalidEntry(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "dev.apiary.example.yaml", validEntryYAML)
	writeEntry(t, dir, "dev.apiary.broken.yaml", "schema_version: 1\nid: dev.apiary.broken\n")
	if _, err := LoadEntries(dir); err == nil {
		t.Fatal("one invalid entry must fail the whole load")
	}
}

func TestCompileIndexStampsConformanceVerdicts(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "dev.apiary.example.yaml", validEntryYAML)
	entries, err := LoadEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	results := ConformanceResults{ConformanceKey("dev.apiary.example", "1.0.0"): {Status: "failed", Kit: "sdk/conformance"}}
	index, err := CompileIndex(entries, results, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	verdict := index.Plugins[0].Releases[0].Conformance
	if verdict == nil || verdict.Status != "failed" {
		t.Fatalf("CI's verdict must reach the index, got %+v", verdict)
	}
}

// Absent evidence is published as absent — never as a pass.
func TestCompileIndexMarksUnrunConformance(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "dev.apiary.example.yaml", validEntryYAML)
	entries, err := LoadEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	index, err := CompileIndex(entries, nil, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := index.Plugins[0].Releases[0].Conformance.Status; got != "not_run" {
		t.Fatalf("want not_run, got %q", got)
	}
}

// The conformance config is CI's business: it must not reach the published
// index, where a client would have no use for it.
func TestCompileIndexOmitsConformanceConfig(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "dev.apiary.example.yaml", validEntryYAML+"conformance_config:\n  path: /tmp/x\n")
	entries, err := LoadEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := entries[0].ConformanceConfigJSON()
	if err != nil || !strings.Contains(config, "/tmp/x") {
		t.Fatalf("CI needs the config as JSON, got %q (%v)", config, err)
	}
	index, err := CompileIndex(entries, nil, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if index.Plugins[0].ConformanceConfig != nil {
		t.Fatal("conformance_config must not be published")
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "conformance_config") || strings.Contains(string(encoded), "/tmp/x") {
		t.Fatalf("conformance_config leaked into the published index: %s", encoded)
	}
}

// Whatever CI publishes has to be something a client would accept, or the
// failure surfaces on operators' machines instead of in CI.
func TestCompileIndexRoundTripsThroughTheClientParser(t *testing.T) {
	entry := &Entry{SchemaVersion: 1, RegistryPlugin: *testEntry(testRelease("1.0.0", ">= 0.1.0-0"))}
	entry.Capabilities = []Capability{"not-a-capability"}
	if _, err := CompileIndex([]*Entry{entry}, nil, time.Unix(0, 0)); err == nil {
		t.Fatal("an index clients would reject must not compile")
	}
}
