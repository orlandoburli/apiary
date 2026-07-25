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

// resolveQueueListenAddress ensures the queue protocol server binds to loopback
// by default. When address has no host component, 127.0.0.1 is used. When the
// host is non-loopback a warning is logged so operators know they have opted in
// to a network-exposed binding.
func resolveQueueListenAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	ip := net.ParseIP(host)
	isLoopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !isLoopback {
		aplog.Warn("queue worker protocol is bound to a non-loopback address %q; set settings.queue.listen to a loopback address (e.g. 127.0.0.1:<port>) unless remote workers are required", address)
	}
	return address
}

func (d *Dispatcher) startQueueProtocolServer(ctx context.Context, wg *sync.WaitGroup) error {
	address := strings.TrimSpace(d.cfg.Settings.Queue.Listen)
	if address == "" {
		return nil
	}
	if d.dispatchQueue == nil {
		return fmt.Errorf("queue protocol listener requires the durable queue")
	}
	address = resolveQueueListenAddress(address)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for queue workers on %s: %w", address, err)
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
