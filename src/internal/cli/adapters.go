package cli

import (
	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/workflow"

	// Ensure the built-in adapters are registered before any command validates
	// a config, even when the cli package is used without cmd/apiary.
	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

func init() {
	config.KnownAdapters = runner.Registered
	config.LintExpr = workflow.LintExpr
	// Evaluated at validation time, after the source adapters' init() registration
	// (cmd/apiary imports them), so a fresh instance reflects the real capability.
	config.SourceSupportsDependencyWait = func(sourceType string) bool {
		a, ok := source.New(sourceType)
		if !ok {
			return false
		}
		_, ok = a.(source.BlockerLister)
		return ok
	}
	config.SourceSupportsPREvents = func(sourceType string) bool {
		a, ok := source.New(sourceType)
		if !ok {
			return false
		}
		_, ok = a.(source.PREventPoller)
		return ok
	}
	// Same pattern for the config-key lint: ask a fresh instance for the
	// config schema it declares, so `apiary validate` rejects a key the
	// adapter would never read instead of letting the source poll
	// misconfigured.
	config.SourceConfigSchema = func(sourceType string) (config.SourceSchema, bool) {
		schema, ok := source.ConfigSchemaFor(sourceType)
		if !ok {
			return config.SourceSchema{}, false
		}
		out := config.SourceSchema{
			Aliases:   schema.Aliases,
			OpenEnded: schema.OpenEnded,
			Keys:      make([]config.SourceSchemaKey, 0, len(schema.Keys)),
		}
		for _, k := range schema.Keys {
			out.Keys = append(out.Keys, config.SourceSchemaKey{
				Name:     k.Name,
				Required: k.Required,
				Secret:   k.Secret,
				Desc:     k.Desc,
			})
		}
		return out, true
	}
	// Same pattern for the write-capability lint: probe a fresh instance for
	// the optional interfaces, so read-only sources (prometheus alerts) reject
	// set_state/add_labels/approvals/wait_for-ci workflows at validation time.
	config.SourceCapabilities = func(sourceType string) config.SourceCaps {
		a, ok := source.New(sourceType)
		if !ok {
			return config.SourceCaps{}
		}
		var caps config.SourceCaps
		_, caps.SetState = a.(source.StateSetter)
		_, caps.AddLabels = a.(source.LabelAdder)
		_, caps.RemoveLabels = a.(source.LabelRemover)
		_, caps.Approvals = a.(source.TaskPoller)
		_, caps.CIWait = a.(source.CIStatusPoller)
		_, caps.PRCIWait = a.(source.PRCIStatusPoller)
		_, caps.SubIssues = a.(source.SubIssueCreator)
		_, caps.Resolvable = a.(source.ItemResolver)
		return caps
	}
}
