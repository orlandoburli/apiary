package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Publishing is a pull request: one YAML file per plugin under
// registry/plugins/, reviewed by a human and verified by CI, compiled into the
// JSON index clients read. There is no upload endpoint and no account — the
// repository's access control is the ownership model.
const EntrySchemaVersion = 1

// Entry is the on-disk form of a registry listing.
type Entry struct {
	SchemaVersion  int `yaml:"schema_version"`
	RegistryPlugin `yaml:",inline"`
}

// LoadEntry reads and validates one entry file. The filename must be the plugin
// id, so a listing cannot be reviewed under one name and published under
// another.
func LoadEntry(path string) (*Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entry Entry
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&entry); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if entry.SchemaVersion != EntrySchemaVersion {
		return nil, fmt.Errorf("%s: unsupported schema_version %d; expected %d", path, entry.SchemaVersion, EntrySchemaVersion)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base != entry.ID {
		return nil, fmt.Errorf("%s: filename must be %q.yaml to match the plugin id", path, entry.ID)
	}
	if errs := entry.Validate(); len(errs) > 0 {
		messages := make([]string, len(errs))
		for i, err := range errs {
			messages[i] = err.Error()
		}
		return nil, fmt.Errorf("%s:\n  - %s", path, strings.Join(messages, "\n  - "))
	}
	if _, err := entry.ConformanceConfigJSON(); err != nil {
		return nil, fmt.Errorf("%s: conformance_config: %w", path, err)
	}
	return &entry, nil
}

// ConformanceConfigJSON renders the entry's conformance configuration as the
// JSON the kit expects on the wire. Empty when the entry declares none, in
// which case CI records the release as "conformance not run" rather than
// inventing a configuration the plugin never agreed to.
func (p *RegistryPlugin) ConformanceConfigJSON() (string, error) {
	if len(p.ConformanceConfig) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(p.ConformanceConfig)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// LoadEntries reads every entry in a directory, failing on the first invalid
// one. A registry that partially parses would publish exactly the listings
// nobody checked.
func LoadEntries(dir string) ([]*Entry, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	entries := make([]*Entry, 0, len(matches))
	seen := map[string]string{}
	for _, path := range matches {
		entry, err := LoadEntry(path)
		if err != nil {
			return nil, err
		}
		if previous, exists := seen[entry.ID]; exists {
			return nil, fmt.Errorf("%s: plugin id %q is already listed by %s", path, entry.ID, previous)
		}
		seen[entry.ID] = path
		entries = append(entries, entry)
	}
	return entries, nil
}

// ConformanceResults maps "<id>@<version>" to the verdict CI observed. It is
// produced by the checker and merged at build time, so a submitter cannot
// declare their own plugin conformant.
type ConformanceResults map[string]Conformance

// ConformanceKey is the results key for one release.
func ConformanceKey(id, version string) string { return id + "@" + version }

// CompileIndex turns reviewed entries into the document clients fetch, stamping
// CI's conformance verdicts onto the releases they were observed against.
func CompileIndex(entries []*Entry, results ConformanceResults, generatedAt time.Time) (*Index, error) {
	index := &Index{SchemaVersion: IndexSchemaVersion, GeneratedAt: generatedAt.UTC().Format(time.RFC3339)}
	for _, entry := range entries {
		published := entry.RegistryPlugin
		// CI's business, not the client's: it is excluded from the JSON anyway,
		// and dropping it here keeps the published value and the serialized one
		// telling the same story.
		published.ConformanceConfig = nil
		published.Releases = make([]Release, len(entry.Releases))
		copy(published.Releases, entry.Releases)
		for i := range published.Releases {
			release := &published.Releases[i]
			verdict, ok := results[ConformanceKey(entry.ID, release.Version)]
			if !ok {
				verdict = Conformance{Status: "not_run"}
			}
			release.Conformance = &verdict
		}
		index.Plugins = append(index.Plugins, published)
	}
	sort.Slice(index.Plugins, func(i, j int) bool { return index.Plugins[i].ID < index.Plugins[j].ID })
	// Round-trip through the client's own parser: whatever CI publishes must be
	// something a client would accept, or the failure surfaces on operators'
	// machines instead of in CI.
	encoded, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	if _, err := ParseIndex(encoded); err != nil {
		return nil, fmt.Errorf("compiled index would be rejected by clients: %w", err)
	}
	return index, nil
}
