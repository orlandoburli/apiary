package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
)

// daemonProbeTimeout bounds the liveness probe. It is short on purpose: the
// socket is local, so a daemon that is up answers immediately, and a slow
// answer must not delay a migration on a hive that is down.
const daemonProbeTimeout = 2 * time.Second

// daemonIsServing reports whether a daemon is answering on the control socket
// for the current config.
//
// The daemon removes a stale socket file at startup (Dispatcher.StartServer), so
// a failed dial is a reliable "not running" rather than a leftover from a crash.
// The returned path is the socket that was probed, for use in error messages.
func daemonIsServing(ctx context.Context) (bool, string) {
	socketPath := daemon.SocketPath(config.DataDir(configFile))

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: daemonProbeTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://apiary/health", nil)
	if err != nil {
		return false, socketPath
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, socketPath
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, socketPath
}

// errDaemonRunning is returned when a data migration is attempted while a
// daemon is serving.
type errDaemonRunning struct{ socket string }

func (e errDaemonRunning) Error() string {
	return fmt.Sprintf(
		"a daemon is running (answering on %s)\n\n"+
			"Data migrations rewrite rows, and one recreates a table, so a row the\n"+
			"daemon writes mid-migration can be lost. Stop the daemon first:\n\n"+
			"  apiary service stop\n\n"+
			"then run this again. The daemon also applies pending migrations itself\n"+
			"at startup, so restarting it is enough if you would rather not run them\n"+
			"by hand.",
		e.socket)
}

// refuseIfDaemonRunning blocks a data migration while a daemon is serving on the
// control socket. See #468.
//
// A daemon starting up calls the migration *before* it serves its own socket, so
// this correctly guards a second daemon racing the first rather than blocking
// the normal startup path.
//
// One gap it cannot close: a daemon started against a different config file has
// a different socket path and will not be found, even if it points at the same
// database. That is a pre-existing hazard for every command that probes the
// socket, not one this guard introduces.
func refuseIfDaemonRunning(ctx context.Context) error {
	if serving, socket := daemonIsServing(ctx); serving {
		return errDaemonRunning{socket: socket}
	}
	return nil
}
