package github

import "github.com/orlandoburli/apiary/internal/source"

var _ source.ConfigSchemaProvider = (*Adapter)(nil)

// ConfigSchema declares the `sources[].config` keys Connect reads.
//
// api_key is optional on purpose: a public repository polls fine unauthenticated
// (at 60 req/h). It is the alias table below that catches the motivating typo —
// `token:` on a private repo, which GitHub answers with 404, not 401, so the
// symptom reads as a missing repository rather than a credential never applied.
func (a *Adapter) ConfigSchema() source.ConfigSchema {
	return source.ConfigSchema{
		Keys: []source.ConfigKey{
			{Name: "repo", Required: true, Desc: `repository in "owner/repo" format`},
			{Name: "api_key", Secret: true, Desc: "GitHub personal access token"},
			{Name: "base_url", Desc: "API base URL, for GitHub Enterprise Server"},
		},
		Aliases: map[string]string{
			"token":        "api_key",
			"github_token": "api_key",
			"api_token":    "api_key",
			"access_token": "api_key",
			"pat":          "api_key",
			"repository":   "repo",
			"url":          "base_url",
			"api_url":      "base_url",
		},
	}
}
