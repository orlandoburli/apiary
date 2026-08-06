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
)

// startWebhookServer starts the inbound webhook listener
// (settings.webhook.listen) and mounts every push-capable source — an adapter
// whose WebhookHandler() is non-nil — at POST /webhook/{source-id}. The
// adapter owns authentication (bearer/HMAC) and payload parsing; the daemon
// only routes by path. No-op when settings.webhook.listen is empty (config
// validation already rejects webhook-type sources without it) or when no
// configured source can receive pushes.
func (d *Dispatcher) startWebhookServer(ctx context.Context, wg *sync.WaitGroup) error {
	address := strings.TrimSpace(d.cfg.Settings.Webhook.Listen)
	if address == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mounted := 0
	for id, adapter := range d.sources {
		handler := adapter.WebhookHandler()
		if handler == nil {
			continue
		}
		path := "/webhook/" + id
		mux.Handle(path, handler)
		mounted++
		aplog.Info("webhook: source %s mounted at POST %s", id, path)
	}
	if mounted == 0 {
		aplog.Warn("settings.webhook.listen is set but no configured source accepts pushes — webhook listener not started")
		return nil
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for webhooks on %s: %w", address, err)
	}
	d.webhookAddr.Store(listener.Addr().String())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			aplog.Error("webhook server: %v", err)
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
	aplog.Info("webhook listener on %s (%d source(s) mounted)", listener.Addr(), mounted)
	return nil
}
