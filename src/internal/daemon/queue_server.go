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

	queueCfg := d.cfg.Settings.Queue
	hasTLS := strings.TrimSpace(queueCfg.TLSCertFile) != "" && strings.TrimSpace(queueCfg.TLSKeyFile) != ""

	if !hasTLS && !queueCfg.TLSProxyMode && !isLoopbackOnly(address) {
		return fmt.Errorf(
			"queue protocol listener on %s would expose the worker token in plaintext: "+
				"configure tls_cert_file/tls_key_file for direct TLS, or set tls_proxy_mode: true "+
				"when a TLS-terminating proxy (e.g. nginx, Caddy) fronts this port",
			address,
		)
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for queue workers on %s: %w", address, err)
	}

	if hasTLS {
		cert, err := tls.LoadX509KeyPair(queueCfg.TLSCertFile, queueCfg.TLSKeyFile)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("load queue TLS certificate: %w", err)
		}
		listener = tls.NewListener(listener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
	}

	handler := queuehttp.Server{
		Store: d.dispatchQueue, Token: queueCfg.WorkerToken,
		LeaseDuration: queueCfg.LeaseDurationValue(),
		WorkerTimeout: queueCfg.WorkerTimeoutValue(), Policy: d.queuePolicy(),
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
	scheme := "http"
	if hasTLS {
		scheme = "https"
	}
	aplog.Info("queue worker protocol listening on %s://%s", scheme, listener.Addr())
	return nil
}

// isLoopbackOnly reports whether addr (host:port) binds only to the loopback
// interface. An empty host or a wildcard (0.0.0.0 / ::) is NOT loopback-only.
func isLoopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}
