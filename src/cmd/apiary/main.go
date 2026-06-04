package main

import (
	"github.com/orlandoburli/apiary/internal/cli"

	_ "github.com/orlandoburli/apiary/internal/source/plane"
	_ "github.com/orlandoburli/apiary/internal/source/github"

	_ "github.com/orlandoburli/apiary/internal/runner/cli"
	_ "github.com/orlandoburli/apiary/internal/runner/script"
	_ "github.com/orlandoburli/apiary/internal/runner/opencode"
)

func main() {
	cli.Execute()
}
