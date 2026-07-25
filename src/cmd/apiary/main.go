package main

import (
	"github.com/orlandoburli/apiary/internal/cli"
	"github.com/orlandoburli/apiary/internal/plugin"

	_ "github.com/orlandoburli/apiary/internal/source/github"
	_ "github.com/orlandoburli/apiary/internal/source/jira"
	_ "github.com/orlandoburli/apiary/internal/source/plane"

	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

func main() {
	// Landlock re-exec trampoline: when the host binary is re-invoked as a
	// sandbox launcher it applies filesystem restrictions and exec()s the plugin.
	// This check must precede all other initialization.
	if plugin.IsSandboxLauncher() {
		plugin.RunSandboxLauncher()
	}
	cli.Execute()
}
