package dynatrace

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "base_url", Required: true, Desc: "Dynatrace environment URL"},
			{Name: "api_token", Required: true, Secret: true, Desc: "API token with the problems.read scope"},
			{Name: "max_new_per_poll", Desc: "storm cap: max new problems dispatched per poll"},
			{Name: "min_age", Desc: "flap dampener: minimum problem age before dispatch"},
			{Name: "lookback", Desc: "how far back the problem query window reaches"},
		},
		Aliases: map[string]string{
			"token":        "api_token",
			"api_key":      "api_token",
			"access_token": "api_token",
			"url":          "base_url",
			"api_url":      "base_url",
			"environment":  "base_url",
		},
	}
}
