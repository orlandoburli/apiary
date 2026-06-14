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
}
