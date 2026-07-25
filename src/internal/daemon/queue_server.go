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

func (d *Dispatcher) startQueueProtocolServer(ctx context.Context, wg *sync.WaitGroup) error {
	address := strings.TrimSpace(d.cfg.Settings.Queue.Listen)
	if address == "" {
		return nil
	}
	if d.dispatchQueue == nil {
		return fmt.Errorf("queue protocol listener requires the durable queue")
	}
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
	certFile := strings.TrimSpace(d.cfg.Settings.Queue.TLSCertFile)
	keyFile := strings.TrimSpace(d.cfg.Settings.Queue.TLSKeyFile)
	tlsEnabled := certFile != "" && keyFile != ""
	wg.Add(1)
	go func() {
		defer wg.Done()
		var serveErr error
		if tlsEnabled {
			serveErr = server.ServeTLS(listener, certFile, keyFile)
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			aplog.Error("queue protocol server: %v", serveErr)
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
	if tlsEnabled {
		aplog.Info("queue worker protocol listening on %s (TLS)", listener.Addr())
	} else {
		aplog.Info("queue worker protocol listening on %s", listener.Addr())
	}
	return nil
}
