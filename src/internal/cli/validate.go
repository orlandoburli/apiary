package cli

import (
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var connectivity bool
	var envName string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate apiary.yaml",
		Long: `Validate the apiary.yaml configuration file.

With --env, resolves the named environment's overlays and validates the
resulting configuration in addition to the base config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			errs := cfg.Validate()
			if len(errs) == 0 {
				_, pluginErrs := configuredPlugins(cfg)
				errs = append(errs, pluginErrs...)
			}
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s\n", e)
				}
				return fmt.Errorf("%d validation error(s)", len(errs))
			}

			for _, w := range cfg.WorkflowWarnings() {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠ %s\n", w)
			}

			fmt.Println("✓ base config is valid")

			if envName != "" {
				resolved, err := cfg.ResolveEnvironment(envName)
				if err != nil {
					return err
				}
				envErrs := resolved.Validate()
				if len(envErrs) > 0 {
					for _, e := range envErrs {
						fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [env %s] %s\n", envName, e)
					}
					return fmt.Errorf("environment %q: %d validation error(s)", envName, len(envErrs))
				}
				fmt.Printf("✓ environment %q is valid (digest: %s)\n", envName, resolved.Digest())
			}

			if connectivity {
				fmt.Println("connectivity checks not yet implemented")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connectivity, "connectivity", false, "also test source connectivity")
	cmd.Flags().StringVar(&envName, "env", "", "also validate a named environment and its resolved configuration")
	return cmd
}
