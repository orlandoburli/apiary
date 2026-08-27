package source

import "sort"

// ConfigKey describes one key an adapter reads from a `sources[].config` map.
//
// The list is derived from the adapter's own Connect: a key the adapter never
// reads must not be declared, because the whole point of the schema is that a
// key accepted by validation is a key that actually takes effect.
type ConfigKey struct {
	Name     string // the YAML key, e.g. "api_key"
	Required bool   // Connect fails without it
	Secret   bool   // a credential; documented as such, never echoed in errors
	Desc     string // short human description, used by docs/tests
}

// ConfigSchema is an adapter's declaration of the `sources[].config` keys it
// understands. `apiary validate` rejects an unknown key (silent misconfiguration
// — the daemon runs and does the wrong thing) and a missing required one.
type ConfigSchema struct {
	// Keys are every key the adapter reads.
	Keys []ConfigKey

	// Aliases maps a wrong-but-plausible key to the correct one, so validation
	// can say "did you mean" where plain edit distance cannot: `token` and
	// `api_key` are five edits apart but mean the same thing to a human.
	Aliases map[string]string

	// OpenEnded marks a source type that legitimately forwards arbitrary keys
	// to something else. Unknown keys are then accepted; required keys are
	// still enforced. No built-in source is open-ended today.
	OpenEnded bool
}

// KeyNames returns the declared key names, sorted, for error messages and docs.
func (s ConfigSchema) KeyNames() []string {
	names := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		names = append(names, k.Name)
	}
	sort.Strings(names)
	return names
}

// ConfigSchemaProvider is an optional interface an adapter implements to declare
// its config schema. Adapters that do not implement it are not config-validated.
type ConfigSchemaProvider interface {
	ConfigSchema() ConfigSchema
}

// ConfigSchemaFor returns the declared config schema for a source type. The
// second result is false when the type is unregistered or its adapter declares
// no schema.
func ConfigSchemaFor(sourceType string) (ConfigSchema, bool) {
	a, ok := New(sourceType)
	if !ok {
		return ConfigSchema{}, false
	}
	p, ok := a.(ConfigSchemaProvider)
	if !ok {
		return ConfigSchema{}, false
	}
	return p.ConfigSchema(), true
}
