package pluginsource

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
//
// The bridge is deliberately NOT open-ended: nothing here is forwarded to the
// plugin process. A plugin's own settings live under `plugins[].config` and are
// checked against the plugin manifest's JSON schema, so a key placed here would
// be silently dropped — exactly the failure this validation exists to prevent.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "plugin", Required: true, Desc: "id of an enabled plugins[] instance with the \"source\" capability"},
		},
		Aliases: map[string]string{
			"plugin_id": "plugin",
			"id":        "plugin",
			"instance":  "plugin",
		},
	}
}
