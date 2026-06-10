package cli

import (
	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/runner"

	// Ensure the built-in adapters are registered before any command validates
	// a config, even when the cli package is used without cmd/apiary.
	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

func init() {
	config.KnownAdapters = runner.Registered
}
