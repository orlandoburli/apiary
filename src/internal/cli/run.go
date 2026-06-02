package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/tui"
)

func newRunCmd() *cobra.Command {
	var (
		dryRun bool
		once   bool
		src    string
		worker string
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

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			disp, err := daemon.New(ctx, cfg, configFile)
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

			m := tui.New()
			p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
			if _, tuiErr := p.Run(); tuiErr != nil {
				cancel()
				wg.Wait()
				return fmt.Errorf("tui: %w", tuiErr)
			}

			cancel()
			wg.Wait()
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "connect to sources but do not invoke runners")
	cmd.Flags().BoolVar(&once, "once", false, "poll once, dispatch all matching tasks, then exit (exit 4 if any run failed)")
	cmd.Flags().StringVar(&src, "source", "", "restrict to a single source id")
	cmd.Flags().StringVar(&worker, "worker", "", "restrict to a single worker id")

	return cmd
}
