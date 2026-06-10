package cli

import (
	"fmt"
	"os"
	"path/filepath"

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
		configFile = resolveConfigFile()
		maybeNotifyUpdate(cmd)
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
		newDashboardCmd(),
		newStatusCmd(),
		newValidateCmd(),
		newCellsCmd(),
		newInstancesCmd(),
		newTaskCmd(),
		newResumeCmd(),
		newClearCmd(),
		newDispatchCmd(),
		newServiceCmd(),
		newInitCmd(),
		newVersionCmd(),
		newRestartCmd(),
		newDeleteCmd(),
		newUpdateCmd(),
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

// resolveConfigFile resolves the config file path by trying the default
// location first, then falling back to .apiary/apiary.yaml in the same
// directory. If --config was explicitly provided, it is used as-is.
func resolveConfigFile() string {
	// Detect whether the user passed an explicit --config flag. We compare
	// against the flag default rather than inspect Changed() because the
	// flag may not have been parsed yet for some subcommands.
	const defaultConfig = "apiary.yaml"

	// The user provided a custom path — trust it.
	if configFile != defaultConfig {
		return configFile
	}

	// Try the default location (apiary.yaml in CWD).
	if _, err := os.Stat(configFile); err == nil {
		return configFile
	}

	// Fall back to .apiary/apiary.yaml.
	fallback := filepath.Join(".apiary", defaultConfig)
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}

	// Nothing found — return the default so the caller produces a clear error.
	return configFile
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
