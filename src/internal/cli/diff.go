package cli

import (
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <env1> <env2>",
		Short: "Show semantic diff between two environment configurations",
		Long: `Show a semantic diff between two named environment configurations.

Each environment's overlays are resolved and the resulting configurations are
compared entity-by-entity (workflows, sources, agents, runners, settings).
Use "base" as an environment name to compare against the raw config with no
overlay applied.

Examples:
  apiary diff development staging
  apiary diff staging production
  apiary diff base production`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			resolve := func(name string) (*config.Config, error) {
				if name == "base" {
					return cfg, nil
				}
				if _, ok := cfg.Environments[name]; !ok {
					return nil, fmt.Errorf("environment %q not found in config", name)
				}
				return cfg.ResolveForEnvironment(name)
			}

			fromName, toName := args[0], args[1]
			fromCfg, err := resolve(fromName)
			if err != nil {
				return err
			}
			toCfg, err := resolve(toName)
			if err != nil {
				return err
			}

			fromDigest, _ := config.Digest(fromCfg)
			toDigest, _ := config.Digest(toCfg)

			entries := config.SemanticDiff(fromCfg, toCfg)

			fmt.Printf("diff %s..%s\n", fromName, toName)
			fmt.Printf("  from digest: %s\n", fromDigest)
			fmt.Printf("  to   digest: %s\n\n", toDigest)

			if len(entries) == 0 {
				fmt.Println("no differences")
				return nil
			}

			for _, e := range entries {
				fmt.Println(e.String())
			}
			return nil
		},
	}
	return cmd
}
