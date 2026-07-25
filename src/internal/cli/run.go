package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/logging"
)

func newRunCmd() *cobra.Command {
	var (
		dryRun  bool
		once    bool
		debug   bool
		src     string
		worker  string
		profile string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the Apiary daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return err
			}
			if errs := cfg.Validate(); len(errs) > 0 {
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "  config error: %s\n", e)
				}
				return fmt.Errorf("config validation failed")
			}
			if os.Getuid() == 0 && !cfg.Settings.Security.AllowRoot {
				return fmt.Errorf(
					"refusing to start: apiary is running as root (uid 0).\n" +
						"Running the daemon as root grants every agent CLI subprocess full system access,\n" +
						"which is unsafe when agents process untrusted content (e.g. Jira tickets, GitHub issues).\n" +
						"Run apiary as a non-root user, or set settings.security.allow_root: true in apiary.yaml\n" +
						"only if you understand the risk (e.g. inside an isolated container).",
				)
			}
			for _, w := range cfg.WorkflowWarnings() {
				fmt.Fprintf(os.Stderr, "  ⚠ %s\n", w)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// Initialize database and logger
			dbPath := getDBPath()
			logDir := getLogDir()

			// Create directories
			dbDir := filepath.Dir(dbPath)
			if err := os.MkdirAll(dbDir, 0o700); err != nil {
				return fmt.Errorf("creating database directory: %w", err)
			}
			if err := os.MkdirAll(logDir, 0o700); err != nil {
				return fmt.Errorf("creating log directory: %w", err)
			}

			dbClient, err := db.New(ctx, dbPath)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer dbClient.Close()

			logLevel := logging.LevelInfo
			if debug {
				logLevel = logging.LevelDebug
				aplog.Enable(true)
			}
			rotation := logging.Rotation{
				MaxSizeMB:  cfg.Settings.LogMaxSizeMB,
				MaxBackups: cfg.Settings.LogMaxBackups,
				MaxAgeDays: cfg.Settings.LogMaxAgeDays,
			}
			logger, err := logging.New(logDir, dbClient, logLevel, rotation)
			if err != nil {
				return fmt.Errorf("initializing logger: %w", err)
			}
			defer logger.Close()

			// Persist operational (aplog) messages as service logs so they show
			// up in the dashboard's Logs tab — not just on the run terminal.
			aplog.SetSink(func(level, msg string) {
				switch level {
				case "ERROR":
					logger.Error(context.Background(), msg, "dispatcher")
				case "WARN":
					logger.Warn(context.Background(), msg, "dispatcher")
				case "DEBUG":
					logger.Debug(context.Background(), msg, "dispatcher")
				default:
					logger.Info(context.Background(), msg, "dispatcher")
				}
			})
			defer aplog.SetSink(nil)

			disp, err := daemon.New(ctx, cfg, configFile, dbClient, logger, profile)
			if err != nil {
				return fmt.Errorf("initialising dispatcher: %w", err)
			}

			_ = src
			_ = worker

			// ── dry-run mode ──────────────────────────────────────────────
			if dryRun {
				return disp.DryRun(ctx)
			}

			// ── once mode ────────────────────────────────────────────────
			if once {
				if err := disp.RunOnce(ctx); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(4)
				}
				return nil
			}

			// ── daemon mode ───────────────────────────────────────────────
			var wg sync.WaitGroup

			if err := disp.StartServer(ctx, &wg); err != nil {
				log.Printf("[apiary] IPC server unavailable: %v", err)
			}

			disp.Start(ctx, &wg)

			level := "info"
			if debug {
				level = "debug"
			}
			fmt.Fprintf(os.Stderr, "apiary running (%s) — logs at %s. Run `apiary dashboard` to watch. Press Ctrl+C to stop.\n", level, logDir)

			// Block until interrupted (SIGINT/SIGTERM via signal.NotifyContext).
			<-ctx.Done()

			cancel()
			wg.Wait()
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "connect to sources but do not invoke runners")
	cmd.Flags().BoolVar(&once, "once", false, "poll once, dispatch all matching tasks, then exit (exit 4 if any run failed)")
	cmd.Flags().BoolVar(&debug, "debug", false, "verbose DEBUG logging: per-task prompt, live agent conversation, and routing decisions (view in the dashboard)")
	cmd.Flags().StringVar(&src, "source", "", "restrict to a single source id")
	cmd.Flags().StringVar(&worker, "worker", "", "restrict to a single worker id")
	cmd.Flags().StringVar(&profile, "profile", "", "activate a named runner profile from config profiles.<name>")

	return cmd
}

// projectDataDir returns the project-scoped data directory (a `.apiary` folder
// alongside the active --config file). See config.DataDir.
func projectDataDir() string {
	return config.DataDir(configFile)
}

// getDBPath returns the path to the SQLite database (<config-dir>/.apiary/apiary.db)
func getDBPath() string {
	return filepath.Join(projectDataDir(), "apiary.db")
}

// getLogDir returns the log directory (<config-dir>/.apiary/logs)
func getLogDir() string {
	return filepath.Join(projectDataDir(), "logs")
}
