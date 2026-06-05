package cli

import (
	"fmt"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	var connectivity bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate apiary.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}

			errs := cfg.Validate()
			if len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s\n", e)
				}
				return fmt.Errorf("%d validation error(s)", len(errs))
			}

			for _, w := range cfg.WorkflowWarnings() {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠ %s\n", w)
			}

			fmt.Println("✓ config is valid")

			if connectivity {
				fmt.Println("connectivity checks not yet implemented")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connectivity, "connectivity", false, "also test source connectivity")
	return cmd
}
