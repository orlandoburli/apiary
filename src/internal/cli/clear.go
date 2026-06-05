package cli

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

func newClearCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Reset the project's SQLite database",
		Long: "Delete the project's SQLite database — task history, executions, workflow " +
			"instances, step runs, and logs. The database is recreated empty on the next run.\n\n" +
			"Refuses to run while the daemon is up (clearing a live database would corrupt it); " +
			"stop the daemon first. Pass --yes to skip the confirmation prompt.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClear(yes)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func runClear(yes bool) error {
	dbPath := getDBPath()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println(instMuted.Render("Nothing to clear — no database at ") + dbPath)
		return nil
	}

	// Clearing the database out from under a running daemon would corrupt its
	// in-flight state. A stale socket with no listener is fine (returns false).
	if daemonIsRunning() {
		fmt.Println(instErr.Render("The daemon appears to be running.") +
			" Stop it first (apiary service stop), then retry.")
		os.Exit(1)
	}

	if !yes {
		fmt.Println(instWarn.Render("This permanently deletes all Apiary state for this project:"))
		fmt.Println("  " + dbPath)
		if !confirm(instWarn.Render("Reset the database?") + " [y/N] ") {
			fmt.Println(instMuted.Render("Aborted."))
			return nil
		}
	}

	removed, err := resetDatabase(dbPath)
	if err != nil {
		fmt.Println(instErr.Render("Failed to clear database: ") + err.Error())
		os.Exit(1)
	}

	fmt.Println(instOK.Render("✓ Database cleared.") + " " +
		instMuted.Render(fmt.Sprintf("(removed %d file(s); it will be recreated on the next run)", removed)))
	return nil
}

// resetDatabase removes the SQLite database file and any WAL/SHM/journal
// sidecars left by an unclean shutdown. The schema is recreated on the next
// `apiary run`. Returns the number of files actually removed.
func resetDatabase(dbPath string) (int, error) {
	removed := 0
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		switch err := os.Remove(p); {
		case err == nil:
			removed++
		case os.IsNotExist(err):
			// sidecar absent — nothing to remove
		default:
			return removed, err
		}
	}
	return removed, nil
}

// daemonIsRunning reports whether a daemon is listening on the project's IPC
// socket. A missing or stale socket (no listener) returns false.
func daemonIsRunning() bool {
	socketPath := daemon.SocketPath(config.DataDir(configFile))
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
