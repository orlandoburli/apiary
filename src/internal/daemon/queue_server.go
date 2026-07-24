package daemon

import (
	"context"
	"crypto/tls"
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

	qs := d.cfg.Settings.Queue
	var listener net.Listener
	var proto string
	if qs.TLSEnabled() {
		cert, err := tls.LoadX509KeyPair(qs.TLSCertFile, qs.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS certificate for queue listener: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		listener, err = tls.Listen("tcp", address, tlsCfg)
		if err != nil {
			return fmt.Errorf("listen (TLS) for queue workers on %s: %w", address, err)
		}
		proto = "https"
	} else {
		var err error
		listener, err = net.Listen("tcp", address)
		if err != nil {
			return fmt.Errorf("listen for queue workers on %s: %w", address, err)
		}
		proto = "http"
	}

	handler := queuehttp.Server{
		Store: d.dispatchQueue, Token: qs.WorkerToken,
		LeaseDuration: qs.LeaseDurationValue(),
		WorkerTimeout: qs.WorkerTimeoutValue(), Policy: d.queuePolicy(),
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
	aplog.Info("queue worker protocol listening on %s://%s", proto, listener.Addr())
	return nil
}
