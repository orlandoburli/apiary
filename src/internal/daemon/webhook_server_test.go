package daemon

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// pushAdapter is a minimal push-capable source: it records deliveries and
// exposes a trivial WebhookHandler.
type pushAdapter struct {
	mu       sync.Mutex
	received []string
}

func (a *pushAdapter) ID() string                                    { return "push" }
func (a *pushAdapter) Connect(context.Context, map[string]any) error { return nil }
func (a *pushAdapter) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (a *pushAdapter) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (a *pushAdapter) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (a *pushAdapter) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.received = append(a.received, string(body))
		a.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
}

func TestStartWebhookServerRoutesBySource(t *testing.T) {
	push := &pushAdapter{}
	poll := &fanoutAdapter{}
	d := &Dispatcher{
		cfg: &config.Config{Settings: config.Settings{
			Webhook: config.WebhookSettings{Listen: "127.0.0.1:0"},
		}},
		sources: map[string]source.Adapter{"push-src": push, "poll-src": poll},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	// Cancel first, then wait: the server goroutines only exit on ctx.Done().
	defer wg.Wait()
	defer cancel()
	if err := d.startWebhookServer(ctx, &wg); err != nil {
		t.Fatalf("startWebhookServer: %v", err)
	}

	base := "http://" + d.webhookAddr.Load().(string)

	resp, err := http.Post(base+"/webhook/push-src", "application/json", strings.NewReader(`{"id":"e1"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("push-src: got %d, want 202", resp.StatusCode)
	}
	push.mu.Lock()
	got := len(push.received)
	push.mu.Unlock()
	if got != 1 {
		t.Errorf("adapter received %d deliveries, want 1", got)
	}

	// Poll-only sources are not mounted; unknown paths 404.
	for _, path := range []string{"/webhook/poll-src", "/webhook/nope"} {
		resp, err := http.Post(base+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, resp.StatusCode)
		}
	}

	resp, err = http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: got %d, want 200", resp.StatusCode)
	}
}

func TestStartWebhookServerNoops(t *testing.T) {
	// Empty listen: no-op.
	d := &Dispatcher{cfg: &config.Config{}, sources: map[string]source.Adapter{"push": &pushAdapter{}}}
	var wg sync.WaitGroup
	if err := d.startWebhookServer(context.Background(), &wg); err != nil {
		t.Errorf("empty listen: %v", err)
	}

	// Listen set but no push-capable source: warns and does not listen.
	d = &Dispatcher{
		cfg: &config.Config{Settings: config.Settings{
			Webhook: config.WebhookSettings{Listen: "127.0.0.1:0"},
		}},
		sources: map[string]source.Adapter{"poll": &fanoutAdapter{}},
	}
	if err := d.startWebhookServer(context.Background(), &wg); err != nil {
		t.Errorf("no push sources: %v", err)
	}
	wg.Wait()
}
