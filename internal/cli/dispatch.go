package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDispatchCmd() *cobra.Command {
	var (
		cell   string
		worker string
	)

	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Manually dispatch a task to a worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cell == "" {
				return fmt.Errorf("--cell is required (format: <source-id>/<task-id>)")
			}
			if worker == "" {
				return fmt.Errorf("--worker is required")
			}
			fmt.Printf("dispatching cell %q to worker %q (not yet implemented)\n", cell, worker)
			return nil
		},
	}

	cmd.Flags().StringVar(&cell, "cell", "", "cell to dispatch (source-id/task-id)")
	cmd.Flags().StringVar(&worker, "worker", "", "worker id to invoke")
	return cmd
}
