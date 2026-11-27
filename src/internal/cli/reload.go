package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

func newReloadCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Reload the daemon configuration without restarting it",
		Long: "Ask the running daemon to re-read apiary.yaml.\n\n" +
			"Workflows, agents, and settings that are read on every use apply immediately.\n" +
			"Sources and settings the daemon reads once at startup — plugin_dirs, log\n" +
			"retention, git_hooks, queue, memory, events — are reported as requiring a\n" +
			"restart when they change. A config that does not load or validate is\n" +
			"rejected whole: the daemon keeps running its previous configuration.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			if code := runReload(asJSON, os.Stdout); code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

// runReload posts /reload and renders the result, returning the process exit
// code so the command (and its tests) do not depend on os.Exit.
func runReload(asJSON bool, out io.Writer) int {
	result, _, err := fetchReloadResult()
	if err != nil {
		fmt.Fprintln(out, instErr.Render("Cannot reload: ")+err.Error())
		return 1
	}

	if asJSON {
		_ = json.NewEncoder(out).Encode(result)
	} else {
		renderReloadResult(out, result)
	}
	if !result.Applied {
		return 1
	}
	return 0
}

func renderReloadResult(out io.Writer, result *daemon.ReloadResult) {
	if result.Applied {
		fmt.Fprintln(out, instOK.Render("✓ config reloaded"))
	} else {
		msg := result.Error
		if msg == "" {
			msg = "the daemon rejected the config"
		}
		fmt.Fprintln(out, instErr.Render("✗ reload rejected: ")+msg)
	}

	for _, line := range []struct {
		name string
		set  daemon.ChangeSet
	}{
		{"workflows", result.Workflows},
		{"agents", result.Agents},
		{"sources", result.Sources},
	} {
		if text := changeLine(line.name, line.set); text != "" {
			fmt.Fprintln(out, "  "+text)
		}
	}
	if len(result.SettingsChanged) > 0 {
		fmt.Fprintln(out, "  settings: "+strings.Join(result.SettingsChanged, ", "))
	}
	if len(result.RequiresRestart) > 0 {
		fmt.Fprintln(out, "  "+instWarn.Render("requires restart: ")+
			strings.Join(result.RequiresRestart, ", "))
	}
	if !result.Applied {
		fmt.Fprintln(out, "  "+instMuted.Render("the previous configuration is still running"))
	}
}

// changeLine renders one ChangeSet as "workflows: +a ~b -c". Empty parts are
// omitted so a pure addition does not read as a removal.
func changeLine(name string, set daemon.ChangeSet) string {
	var parts []string
	if len(set.Added) > 0 {
		parts = append(parts, "+"+strings.Join(set.Added, ","))
	}
	if len(set.Changed) > 0 {
		parts = append(parts, "~"+strings.Join(set.Changed, ","))
	}
	if len(set.Removed) > 0 {
		parts = append(parts, "-"+strings.Join(set.Removed, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return name + ": " + strings.Join(parts, " ")
}

// fetchReloadResult posts /reload and decodes the body whatever the status:
// a rejection (409) still carries the diff and the error that explain it.
func fetchReloadResult() (*daemon.ReloadResult, int, error) {
	socketPath := daemon.SocketPath(config.DataDir(configFile))
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	// Reloading validates the config and can touch plugin discovery; give it
	// more room than a status probe, but still fail rather than hang.
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}

	req, err := http.NewRequest(http.MethodPost, "http://apiary/reload", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w — is the daemon running?", err)
	}
	defer resp.Body.Close()

	var result daemon.ReloadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decoding the daemon response: %w", err)
	}
	return &result, resp.StatusCode, nil
}
