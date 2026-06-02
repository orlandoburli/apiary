package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status and active runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Apiary v0.1.0-dev")
			fmt.Println()
			fmt.Println("Daemon is not running. Use 'apiary run' to start.")
			_ = watch
			return nil
		},
	}

	cmd.Flags().BoolVar(&watch, "watch", false, "watch and refresh status")
	return cmd
}
