package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCellsCmd() *cobra.Command {
	var (
		source    string
		unmatched bool
		limit     int
	)

	cmd := &cobra.Command{
		Use:   "cells",
		Short: "List tasks visible to Apiary",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("No cells found. Configure a source in apiary.yaml and start the daemon.")
			_ = source
			_ = unmatched
			_ = limit
			return nil
		},
	}

	cmd.Flags().StringVar(&source, "source", "", "filter by source id")
	cmd.Flags().BoolVar(&unmatched, "unmatched", false, "show only tasks matching no route")
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows")
	return cmd
}
