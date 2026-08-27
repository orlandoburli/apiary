package config

import (
	"fmt"
	"sort"
	"strings"
)

// SourceSchemaKey mirrors source.ConfigKey. The config package cannot import
// the source package without inverting the dependency direction, so the cli
// package translates the adapters' declarations into these types.
type SourceSchemaKey struct {
	Name     string
	Required bool
	Secret   bool
	Desc     string
}

// SourceSchema mirrors source.ConfigSchema: the `sources[].config` keys one
// source type's adapter actually reads.
type SourceSchema struct {
	Keys      []SourceSchemaKey
	Aliases   map[string]string
	OpenEnded bool
}

// SourceConfigSchema reports the declared config schema of a source type's
// adapter. The cli package injects it (config cannot import the source package
// without inverting the dependency direction). When nil — configs built in
// code, isolated tests — the config-key check is skipped, mirroring
// KnownAdapters. The second result is false for an unregistered source type or
// an adapter that declares no schema; the check is then skipped for that source.
var SourceConfigSchema func(sourceType string) (SourceSchema, bool)

// keyNames returns the schema's key names, sorted, for error messages.
func (s SourceSchema) keyNames() []string {
	names := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		names = append(names, k.Name)
	}
	sort.Strings(names)
	return names
}

// suggest returns the key the operator most likely meant, or "".
//
// The alias table is consulted first: `token` and `api_key` mean the same thing
// to a human but are five edits apart, and that pair is the reason this check
// exists. Edit distance then catches ordinary typos (`base_ul`, `repos`).
func (s SourceSchema) suggest(key string) string {
	norm := strings.ToLower(strings.TrimSpace(key))
	if alias, ok := s.Aliases[norm]; ok {
		return alias
	}
	best, bestDist := "", 0
	for _, name := range s.keyNames() {
		d := levenshtein(norm, name)
		if d > editBudget(name) {
			continue
		}
		if best == "" || d < bestDist {
			best, bestDist = name, d
		}
	}
	return best
}

// editBudget is how far a mistyped key may stray from a known one before the
// suggestion becomes noise: one edit for short keys, two for longer ones.
func editBudget(name string) int {
	if len(name) <= 4 {
		return 1
	}
	return 2
}

// levenshtein returns the edit distance between a and b.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// A required key is satisfied by being written at all: the check is that the
// key exists, never that its value is non-empty. `api_key: ${GITHUB_TOKEN}`
// expands to nothing whenever the variable is not exported, which YAML then
// reads as a null value, and `apiary validate` must stay runnable in a shell
// (or a CI job) that does not hold the hive's secrets. An unset variable is
// still caught at daemon start, where the credential is actually used.

// validateSourceConfig checks one source's `config` map against its adapter's
// declared schema: unknown keys and missing required keys both fail, because a
// source that polls with a key the adapter never reads runs misconfigured while
// looking healthy.
//
// requiredHandledElsewhere names required keys whose absence Validate already
// reports with a more specific message, so the operator does not see the same
// problem twice.
func validateSourceConfig(scope, sourceType string, cfg map[string]any, requiredHandledElsewhere map[string]bool) []error {
	if SourceConfigSchema == nil || sourceType == "" {
		return nil
	}
	schema, ok := SourceConfigSchema(sourceType)
	if !ok {
		return nil
	}

	accepted := fmt.Sprintf("accepted keys for type %q: %s", sourceType, strings.Join(schema.keyNames(), ", "))
	known := map[string]bool{}
	for _, k := range schema.Keys {
		known[k.Name] = true
	}

	var errs []error

	if !schema.OpenEnded {
		unknown := make([]string, 0, len(cfg))
		for key := range cfg {
			if !known[key] {
				unknown = append(unknown, key)
			}
		}
		sort.Strings(unknown)
		for _, key := range unknown {
			if s := schema.suggest(key); s != "" {
				errs = append(errs, fmt.Errorf("%s: config: unknown key %q — did you mean %q? %s", scope, key, s, accepted))
				continue
			}
			errs = append(errs, fmt.Errorf("%s: config: unknown key %q; %s", scope, key, accepted))
		}
	}

	for _, k := range schema.Keys {
		if !k.Required || requiredHandledElsewhere[k.Name] {
			continue
		}
		if _, present := cfg[k.Name]; present {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: config: missing required key %q (%s); %s", scope, k.Name, k.Desc, accepted))
	}

	return errs
}
