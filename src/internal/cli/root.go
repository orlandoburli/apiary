package cli

import (
	"os"

	"github.com/spf13/cobra"
)

var configFile string

var rootCmd = &cobra.Command{
	Use:   "apiary",
	Short: "Task-driven agent orchestration",
	Long:  "Apiary routes tasks from project management tools to AI agent runners based on declarative rules.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "apiary.yaml", "config file path")

	rootCmd.AddCommand(
		newRunCmd(),
		newStatusCmd(),
		newValidateCmd(),
		newCellsCmd(),
		newDispatchCmd(),
		newServiceCmd(),
		newInitCmd(),
	)
}
