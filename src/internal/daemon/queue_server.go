package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/queuehttp"
)

// resolveQueueListenAddress normalises a raw listen address so that the queue
// server defaults to loopback when no host is specified, and returns whether
// the resolved host is non-loopback (requiring a warning).
func resolveQueueListenAddress(address string) (resolved string, nonLoopback bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Not a standard host:port pair — pass through unchanged.
		return address, false
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port), false
	}
	if host == "localhost" {
		return address, false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return address, false
	}
	return address, true
}

func (d *Dispatcher) startQueueProtocolServer(ctx context.Context, wg *sync.WaitGroup) error {
	address := strings.TrimSpace(d.cfg.Settings.Queue.Listen)
	if address == "" {
		return nil
	}
	resolved, nonLoopback := resolveQueueListenAddress(address)
	if nonLoopback {
		aplog.Warn("queue worker protocol is binding on a non-loopback address (%s); set settings.queue.listen to 127.0.0.1:<port> to restrict access to local connections only", resolved)
	}
	if d.dispatchQueue == nil {
		return fmt.Errorf("queue protocol listener requires the durable queue")
	}
	listener, err := net.Listen("tcp", resolved)
	if err != nil {
		return fmt.Errorf("listen for queue workers on %s: %w", resolved, err)
	}
	handler := queuehttp.Server{
		Store: d.dispatchQueue, Token: d.cfg.Settings.Queue.WorkerToken,
		LeaseDuration: d.cfg.Settings.Queue.LeaseDurationValue(),
		WorkerTimeout: d.cfg.Settings.Queue.WorkerTimeoutValue(), Policy: d.queuePolicy(),
		OnTerminal: d.settleRemoteQueueJob,
	}.Handler()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			aplog.Error("queue protocol server: %v", err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	aplog.Info("queue worker protocol listening on %s", listener.Addr())
	return nil
}
