package cli

import (
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var (
		connectivity bool
		envName      string
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate apiary.yaml",
		Long: `Validate the apiary.yaml configuration file.

When --env is provided, the named environment's overlays are applied first and
the resolved configuration is validated. This catches overlay errors such as
references to undefined source IDs or invalid rollout percentages.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			resolved := cfg
			if envName != "" {
				resolved, err = cfg.ResolveForEnvironment(envName)
				if err != nil {
					return err
				}
				fmt.Printf("Validating resolved config for environment %q\n", envName)
			}

			errs := resolved.Validate()
			if len(errs) == 0 {
				_, pluginErrs := configuredPlugins(resolved)
				errs = append(errs, pluginErrs...)
			}
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s\n", e)
				}
				return fmt.Errorf("%d validation error(s)", len(errs))
			}

			for _, w := range resolved.WorkflowWarnings() {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠ %s\n", w)
			}

			if envName != "" {
				digest, _ := config.Digest(resolved)
				fmt.Printf("✓ config is valid for environment %q (digest: %s)\n", envName, digest)
			} else {
				fmt.Println("✓ config is valid")
			}

			if connectivity {
				fmt.Println("connectivity checks not yet implemented")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connectivity, "connectivity", false, "also test source connectivity")
	cmd.Flags().StringVar(&envName, "env", "", "validate the resolved configuration for this named environment")
	return cmd
}
