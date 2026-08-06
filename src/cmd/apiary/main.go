package main

import (
	"github.com/orlandoburli/apiary/internal/cli"

	_ "github.com/orlandoburli/apiary/internal/source/dynatrace"
	_ "github.com/orlandoburli/apiary/internal/source/github"
	_ "github.com/orlandoburli/apiary/internal/source/jira"
	_ "github.com/orlandoburli/apiary/internal/source/plane"
	_ "github.com/orlandoburli/apiary/internal/source/pluginsource"
	_ "github.com/orlandoburli/apiary/internal/source/prometheus"

	_ "github.com/orlandoburli/apiary/internal/runner/providers"
)

func main() {
	cli.Execute()
}
