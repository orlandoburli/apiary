// Package webhook provides a push-mode source adapter: anything that can POST
// JSON (Alertmanager webhook_configs, Loki ruler, Elastic Watcher, custom
// scripts) delivers events to the daemon's webhook listener, and each accepted
// event becomes an Apiary SourceItem routed through the normal workflow
// trigger matching (labels, states, title_regex).
//
// The adapter itself owns request authentication (shared-secret bearer or
// HMAC signatures with replay protection), payload parsing (generic JSON or
// the Alertmanager webhook format), and a bounded in-memory queue that the
// dispatcher drains via Poll. The daemon mounts WebhookHandler() at
// POST /webhook/{source-id} on the listener configured by
// settings.webhook.listen (see daemon.startWebhookServer).
//
// Like the prometheus poll source, webhook events are read-only work items:
// none of the optional write capabilities are implemented, Acknowledge and
// WriteResult are no-ops, and config validation rejects workflows that need
// write capabilities against this source (config.SourceCapabilities).
package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func init() {
	source.Register("webhook", func() source.Adapter { return &Adapter{} })
}

const (
	// defaultMaxPending bounds the in-memory event queue between polls — the
	// push-side storm guardrail, sibling of the prometheus adapter's
	// max_new_per_poll. Overflow deliveries are rejected with 429 so the
	// sender's own retry policy re-delivers once the queue drains.
	defaultMaxPending = 100

	// defaultMaxBodyBytes caps a single delivery's body.
	defaultMaxBodyBytes = 1 << 20 // 1 MiB

	// defaultTolerance is the HMAC timestamp window: signed deliveries older
	// (or further in the future) than this are rejected, and signatures seen
	// within the window are cached to block replays.
	defaultTolerance = 5 * time.Minute
)

// Adapter implements source.Adapter for pushed JSON events.
type Adapter struct {
	id string

	authMode     string // "bearer", "hmac", or "none"
	secret       string
	format       string // "generic" or "alertmanager"
	maxPending   int
	maxBodyBytes int64
	tolerance    time.Duration

	filterStates []string // lowercased; empty = accept any state
	filterLabels []string // normalized "key:value", lowercased; all must match

	mu     sync.Mutex
	queue  []model.SourceItem
	queued map[string]struct{}  // item IDs currently in the queue (drop re-deliveries)
	wake   func()               // set by the dispatcher; nudges an immediate poll
	seen   map[string]time.Time // hmac replay cache: signature → expiry

	now func() time.Time // test hook; time.Now when nil
}

func (a *Adapter) ID() string { return a.id }

// SetID sets the source ID for this adapter.
func (a *Adapter) SetID(id string) { a.id = id }

// SetWake registers a callback invoked whenever a delivery enqueues at least
// one event, so the dispatcher can poll immediately instead of waiting out the
// source's poll interval.
func (a *Adapter) SetWake(f func()) {
	a.mu.Lock()
	a.wake = f
	a.mu.Unlock()
}

// Connect validates config. No outbound connection is made — the adapter only
// receives.
func (a *Adapter) Connect(_ context.Context, cfg map[string]any) error {
	a.secret, _ = cfg["secret"].(string)

	a.authMode = "bearer"
	if v, ok := cfg["auth"]; ok {
		s, _ := v.(string)
		switch s {
		case "bearer", "hmac", "none":
			a.authMode = s
		default:
			return fmt.Errorf("webhook: config.auth must be \"bearer\", \"hmac\", or \"none\", got %v", v)
		}
	}
	if a.authMode == "none" {
		aplog.Warn("webhook %s: auth \"none\" — every POST to this source's path is accepted; only use behind a trusted network boundary", a.id)
	} else if a.secret == "" {
		return fmt.Errorf("webhook: config.secret is required (or set auth: \"none\" explicitly to accept unauthenticated deliveries)")
	}

	a.format = "generic"
	if v, ok := cfg["format"]; ok {
		s, _ := v.(string)
		switch s {
		case "generic", "alertmanager":
			a.format = s
		default:
			return fmt.Errorf("webhook: config.format must be \"generic\" or \"alertmanager\", got %v", v)
		}
	}

	a.maxPending = defaultMaxPending
	if v, ok := cfg["max_pending"]; ok {
		n, err := toInt(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("webhook: config.max_pending must be a positive integer, got %v", v)
		}
		a.maxPending = n
	}

	a.maxBodyBytes = defaultMaxBodyBytes
	if v, ok := cfg["max_body_bytes"]; ok {
		n, err := toInt(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("webhook: config.max_body_bytes must be a positive integer, got %v", v)
		}
		a.maxBodyBytes = int64(n)
	}

	a.tolerance = defaultTolerance
	if v, ok := cfg["tolerance"]; ok {
		s, _ := v.(string)
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("webhook: config.tolerance must be a positive duration (e.g. \"5m\"), got %v", v)
		}
		a.tolerance = d
	}

	a.queued = map[string]struct{}{}
	a.seen = map[string]time.Time{}

	aplog.Info("webhook %s: configured  auth=%s  format=%s  max_pending=%d  max_body_bytes=%d",
		a.id, a.authMode, a.format, a.maxPending, a.maxBodyBytes)
	return nil
}

// SetFilters stores trigger-side filters applied on delivery: an event is
// enqueued only when its state matches filters.states (if set) and it carries
// every filters.labels entry ("key:value" or "key=value").
func (a *Adapter) SetFilters(states, labels []string) {
	for _, s := range states {
		a.filterStates = append(a.filterStates, strings.ToLower(strings.TrimSpace(s)))
	}
	for _, l := range labels {
		a.filterLabels = append(a.filterLabels, normalizeLabel(l))
	}
}

func normalizeLabel(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if k, v, ok := strings.Cut(l, "="); ok {
		return strings.TrimSpace(k) + ":" + strings.TrimSpace(v)
	}
	return l
}

// matchesFilters reports whether an item passes the configured source filters.
func (a *Adapter) matchesFilters(item model.SourceItem) bool {
	if len(a.filterStates) > 0 {
		ok := false
		state := strings.ToLower(item.State)
		for _, s := range a.filterStates {
			if s == state {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(a.filterLabels) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(item.Labels))
	for _, l := range item.Labels {
		have[strings.ToLower(l)] = struct{}{}
	}
	for _, want := range a.filterLabels {
		if _, ok := have[want]; !ok {
			return false
		}
	}
	return true
}

// enqueue adds items to the pending queue, skipping IDs already queued
// (sender re-deliveries between polls) and rejecting overflow past
// max_pending. Returns accepted and dropped-for-overflow counts.
func (a *Adapter) enqueue(items []model.SourceItem) (accepted, dropped int) {
	a.mu.Lock()
	var wake func()
	for _, item := range items {
		if _, ok := a.queued[item.ID]; ok {
			continue // duplicate delivery of an event still awaiting a poll
		}
		if len(a.queue) >= a.maxPending {
			dropped++
			continue
		}
		a.queued[item.ID] = struct{}{}
		a.queue = append(a.queue, item)
		accepted++
	}
	if accepted > 0 {
		wake = a.wake
	}
	a.mu.Unlock()
	if wake != nil {
		wake()
	}
	return accepted, dropped
}

// Poll drains the pending queue. The since parameter is ignored: delivery
// order and dedup are handled at enqueue time, and re-dispatch of an already
// dispatched event ID is prevented downstream by the persisted task/instance
// dedup, exactly as for polled sources.
func (a *Adapter) Poll(_ context.Context, _ time.Time) ([]model.SourceItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := a.queue
	a.queue = nil
	a.queued = map[string]struct{}{}
	return items, nil
}

// Acknowledge is a no-op: a pushed event has no source-side state to set.
func (a *Adapter) Acknowledge(_ context.Context, cell model.SourceItem, action model.AckAction) error {
	aplog.Debug("webhook %s: acknowledge %s (%s) — no-op for pushed events", a.id, cell.LogLabel(), action)
	return nil
}

// WriteResult is a no-op: the sender is fire-and-forget. The intended pattern
// is a workflow step that publishes findings to a ticket source
// (APIARY_PUBLISH / APIARY_SPAWN) instead.
func (a *Adapter) WriteResult(_ context.Context, cell model.SourceItem, result model.RunResult) error {
	aplog.Debug("webhook %s: write result for %s (success=%v) — no-op for pushed events", a.id, cell.LogLabel(), result.Success)
	return nil
}

// WebhookHandler serves POST deliveries: authenticates, parses per the
// configured format, applies source filters, and enqueues. Non-nil even
// before Connect so config validation can probe push capability on a fresh
// instance.
func (a *Adapter) WebhookHandler() http.Handler {
	return http.HandlerFunc(a.serveHTTP)
}

func (a *Adapter) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := readBody(w, r, a.maxBodyBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}

	if err := a.authenticate(r, body); err != nil {
		aplog.Warn("webhook %s: rejected delivery from %s: %v", a.id, r.RemoteAddr, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	items, err := a.parse(body)
	if err != nil {
		aplog.Warn("webhook %s: unparseable delivery from %s: %v", a.id, r.RemoteAddr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filtered := items[:0]
	for _, item := range items {
		if a.matchesFilters(item) {
			filtered = append(filtered, item)
		}
	}

	accepted, dropped := a.enqueue(filtered)
	if dropped > 0 {
		aplog.Warn("webhook %s: queue full — %d event(s) rejected (max_pending=%d); sender should retry", a.id, dropped, a.maxPending)
	}
	if accepted == 0 && dropped > 0 {
		http.Error(w, "queue full, retry later", http.StatusTooManyRequests)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"accepted":%d,"dropped":%d}`, accepted, dropped)
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	}
	return 0, fmt.Errorf("not an integer: %v", v)
}
