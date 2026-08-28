package plugin

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// OfficialRegistryPublicKey is the minisign public key the official index is
// signed with. It is stamped in at build time:
//
//	-X github.com/orlandoburli/apiary/internal/plugin.OfficialRegistryPublicKey=<base64 line>
//
// While it is empty, the official index is fetched unsigned and every command
// says so. That is the honest state for a registry whose signing key does not
// exist yet — quieter than refusing to work, and louder than pretending the
// index was verified.
var OfficialRegistryPublicKey string

// RegistrySource is one entry of plugin_registries. It accepts either a bare
// URL or a mapping carrying that registry's own signing key, so an internal
// mirror can be pinned to a key its operators control:
//
//	plugin_registries:
//	  - https://orlandoburli.com.br/apiary/registry/v1/index.json
//	  - url: file:///opt/apiary/registry/index.json
//	    public_key: RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
type RegistrySource struct {
	URL string `yaml:"url" json:"url"`
	// PublicKey is a minisign public key (the bare base64 line). When set, this
	// registry's index MUST be signed by it — there is no flag to skip that.
	PublicKey string `yaml:"public_key,omitempty" json:"public_key,omitempty"`
}

// UnmarshalYAML accepts the scalar form as well as the mapping form, so
// existing `plugin_registries: [https://…]` configs keep working unchanged.
func (s *RegistrySource) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&s.URL)
	}
	type plain RegistrySource // avoid recursing into this method
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = RegistrySource(decoded)
	return nil
}

// MarshalYAML writes the scalar form back when there is nothing else to say.
func (s RegistrySource) MarshalYAML() (any, error) {
	if s.PublicKey == "" {
		return s.URL, nil
	}
	type plain RegistrySource
	return plain(s), nil
}

// Key returns the public key this source must be verified against: its own if
// it declares one, the embedded official key for the official index, and
// otherwise none — in which case the index is used unverified and said to be.
func (s RegistrySource) Key() string {
	if s.PublicKey != "" {
		return s.PublicKey
	}
	if s.URL == DefaultRegistryURL {
		return OfficialRegistryPublicKey
	}
	return ""
}

// Validate checks one configured registry.
func (s RegistrySource) Validate() error {
	if err := ValidateRegistryURL(s.URL); err != nil {
		return err
	}
	if s.PublicKey != "" {
		if _, err := ParsePublicKey(s.PublicKey); err != nil {
			return fmt.Errorf("public_key: %w", err)
		}
	}
	return nil
}

// DefaultRegistrySources is what an operator who expressed no preference gets.
func DefaultRegistrySources() []RegistrySource {
	return []RegistrySource{{URL: DefaultRegistryURL}}
}
