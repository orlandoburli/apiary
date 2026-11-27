package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/mcp"
)

// newMCPServeCmd runs the built-in MCP server on stdin/stdout. Agents launch it
// from the MCP entry the daemon injects; a human rarely runs it directly. All
// configuration arrives through the environment (APIARY_SOCKET and the per-run
// ids), so there are no flags.
func newMCPServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp-serve",
		Short: "Serve apiary's MCP tools over stdio (launched by agents)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket := os.Getenv("APIARY_SOCKET")
			if socket == "" {
				// MCP configs always pass APIARY_SOCKET. This fallback keeps a
				// manual `apiary mcp-serve` inside a project working.
				socket = daemon.SocketPath(config.DataDir(configFile))
			}
			server := mcp.NewServer(os.Stdin, os.Stdout, mcp.NewDaemonClient(socket))
			return server.Serve(cmd.Context())
		},
	}
}
