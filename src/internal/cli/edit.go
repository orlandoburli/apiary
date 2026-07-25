package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/editor"
	"github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Open the visual workflow editor",
		Long: `Open a browser-based visual editor for apiary.yaml.

The editor lets you create and connect workflow steps through a drag-and-drop
DAG canvas, edit step properties through schema-driven forms, and preview the
resulting YAML before saving. Unsupported YAML constructs are shown in
read-only mode rather than silently discarded.

The server listens on a random loopback port and exits when you send SIGINT
(Ctrl-C) or close the terminal.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configFile)
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			srv, err := editor.NewServer(configFile, cfg)
			if err != nil {
				return fmt.Errorf("creating editor server: %w", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			url, err := srv.Start(ctx)
			if err != nil {
				return fmt.Errorf("starting editor: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Workflow editor running at %s\n", url)
			fmt.Fprintf(cmd.OutOrStdout(), "Press Ctrl-C to stop.\n")

			if !noBrowser {
				// Small delay to let the server start accepting connections.
				time.Sleep(150 * time.Millisecond)
				_ = openBrowser(url)
			}

			// Block until Ctrl-C.
			<-ctx.Done()
			return nil
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open the browser automatically")
	return cmd
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
