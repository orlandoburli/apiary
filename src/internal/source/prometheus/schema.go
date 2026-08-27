package prometheus

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "alertmanager_url", Required: true, Desc: "Alertmanager base URL"},
			{Name: "bearer_token", Secret: true, Desc: "bearer token for Alertmanager"},
			{Name: "basic_auth_user", Desc: "basic auth user for Alertmanager"},
			{Name: "basic_auth_password", Secret: true, Desc: "basic auth password for Alertmanager"},
			{Name: "max_new_per_poll", Desc: "storm cap: max new items dispatched per poll"},
			{Name: "min_age", Desc: "flap dampener: minimum alert age before dispatch"},
			{Name: "dispatch_by", Desc: `"alert" (default) or "group"`},
			{Name: "ack_via_silence", Desc: "silence the alert while it is being investigated"},
			{Name: "silence_duration", Desc: "how long an ack silence lasts"},
		},
		Aliases: map[string]string{
			"url":                 "alertmanager_url",
			"base_url":            "alertmanager_url",
			"alertmanager":        "alertmanager_url",
			"api_url":             "alertmanager_url",
			"token":               "bearer_token",
			"api_key":             "bearer_token",
			"api_token":           "bearer_token",
			"access_token":        "bearer_token",
			"basic_auth_username": "basic_auth_user",
			"basic_auth_pass":     "basic_auth_password",
		},
	}
}
