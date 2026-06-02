package cli

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/version"
)

var configFile string

var rootCmd = &cobra.Command{
	Use:     "apiary",
	Short:   "Task-driven agent orchestration",
	Long:    "Apiary routes tasks from project management tools to AI agent runners based on declarative rules.",
	Version: version.Version,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		v, _ := cmd.Root().PersistentFlags().GetBool("verbose")
		aplog.Enable(v)
		loadDotEnv(cmd)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate("apiary {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&configFile, "config", "apiary.yaml", "config file path")
	rootCmd.PersistentFlags().Bool("verbose", false, "enable verbose (debug) output")
	rootCmd.PersistentFlags().String("env-file", ".env", "path to .env file (silently skipped if not found)")

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

// loadDotEnv loads the .env file if it exists. Already-set environment
// variables take precedence — godotenv.Load does not overwrite them.
func loadDotEnv(cmd *cobra.Command) {
	path, _ := cmd.Root().PersistentFlags().GetString("env-file")
	if path == "" {
		path = ".env"
	}
	if err := godotenv.Load(path); err == nil {
		aplog.Debug("loaded env file: %s", path)
	}
}
