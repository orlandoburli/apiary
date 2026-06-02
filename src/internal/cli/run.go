package cli

import (
	"context"
	"fmt"
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

			disp, err := daemon.New(ctx, cfg)
			if err != nil {
				return fmt.Errorf("initialising dispatcher: %w", err)
			}

			if dryRun {
				fmt.Println("dry-run: sources connected, no runners will be invoked")
				return nil
			}

			_ = src
			_ = worker
			_ = once

			var wg sync.WaitGroup
			disp.Start(ctx, &wg)

			// TUI runs in the foreground; dispatcher runs in background goroutines
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
	cmd.Flags().BoolVar(&once, "once", false, "poll once, process pending tasks, then exit")
	cmd.Flags().StringVar(&src, "source", "", "restrict to a single source id")
	cmd.Flags().StringVar(&worker, "worker", "", "restrict to a single worker id")

	return cmd
}
