package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/version"
)

var configFile string

var rootCmd = &cobra.Command{
	Use:     "apiary",
	Short:   "Task-driven agent orchestration",
	Long:    "Apiary routes tasks from project management tools to AI agent runners based on declarative rules.",
	Version: version.Version,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate("apiary {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&configFile, "config", "apiary.yaml", "config file path")

	rootCmd.AddCommand(
		newRunCmd(),
		newStatusCmd(),
		newValidateCmd(),
		newCellsCmd(),
		newDispatchCmd(),
		newServiceCmd(),
		newInitCmd(),
		newVersionCmd(),
	)
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the apiary version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("apiary %s\n", version.Version)
		},
	}
}
