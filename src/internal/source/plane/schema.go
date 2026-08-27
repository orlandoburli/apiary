package plane

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "api_key", Required: true, Secret: true, Desc: "Plane API key"},
			{Name: "workspace", Required: true, Desc: "workspace slug"},
			{Name: "project", Required: true, Desc: "project id"},
			{Name: "base_url", Desc: "API base URL, for self-hosted Plane"},
		},
		Aliases: map[string]string{
			"token":          "api_key",
			"api_token":      "api_key",
			"access_token":   "api_key",
			"workspace_slug": "workspace",
			"project_id":     "project",
			"url":            "base_url",
			"api_url":        "base_url",
		},
	}
}
