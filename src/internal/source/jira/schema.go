package jira

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "base_url", Required: true, Desc: "Jira Cloud site URL"},
			{Name: "email", Required: true, Desc: "Atlassian account email (Basic auth user)"},
			{Name: "api_token", Required: true, Secret: true, Desc: "Atlassian API token"},
			{Name: "project", Desc: "project key, or list of project keys, to scope polling"},
			{Name: "started_state", Desc: "status the adapter moves an item to when work starts"},
		},
		Aliases: map[string]string{
			"token":        "api_token",
			"api_key":      "api_token",
			"access_token": "api_token",
			"username":     "email",
			"user":         "email",
			"projects":     "project",
			"project_key":  "project",
			"url":          "base_url",
			"site":         "base_url",
			"api_url":      "base_url",
		},
	}
}
