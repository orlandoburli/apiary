package prometheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// silenceServer serves alerts on GET /api/v2/alerts and records every silence
// posted to POST /api/v2/silences.
type silenceServer struct {
	*httptest.Server

	mu       sync.Mutex
	posted   []silence
	failWith int // when non-zero, silences are rejected with this status
}

func newSilenceServer(t *testing.T, alerts *[]map[string]any) *silenceServer {
	t.Helper()
	s := &silenceServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/alerts":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(*alerts)

		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
			s.mu.Lock()
			fail := s.failWith
			s.mu.Unlock()
			if fail != 0 {
				http.Error(w, "nope", fail)
				return
			}
			var body silence
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.posted = append(s.posted, body)
			n := len(s.posted)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"silenceID": "sil-" + string(rune('0'+n))})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *silenceServer) silences() []silence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]silence(nil), s.posted...)
}

func (s *silenceServer) rejectWith(status int) {
	s.mu.Lock()
	s.failWith = status
	s.mu.Unlock()
}

// pollOne polls the adapter and returns the single item it surfaced.
func pollOne(t *testing.T, a *Adapter) model.SourceItem {
	t.Helper()
	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	return items[0]
}

func matcherMap(t *testing.T, s silence) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, mt := range s.Matchers {
		if mt.IsRegex {
			t.Errorf("matcher %q must be exact equality, got a regex", mt.Name)
		}
		if !mt.IsEqual {
			t.Errorf("matcher %q must be a positive match (isEqual)", mt.Name)
		}
		m[mt.Name] = mt.Value
	}
	return m
}

// The default is unchanged: acknowledging an alert writes nothing back.
func TestAcknowledgeNoSilenceByDefault(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, nil)

	item := pollOne(t, a)
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if got := srv.silences(); len(got) != 0 {
		t.Fatalf("expected no silence without ack_via_silence, got %d", len(got))
	}
}

// With the opt-in on, a dispatched alert is silenced on its exact label set.
func TestAcknowledgeCreatesSilence(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true, "silence_duration": "30m"})

	item := pollOne(t, a)
	before := time.Now()
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	got := srv.silences()
	if len(got) != 1 {
		t.Fatalf("expected 1 silence, got %d", len(got))
	}
	s := got[0]

	want := map[string]string{"alertname": "HighErrorRate", "severity": "critical", "team": "platform"}
	m := matcherMap(t, s)
	if len(m) != len(want) {
		t.Fatalf("matchers = %v, want %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("matcher %s = %q, want %q", k, m[k], v)
		}
	}

	if d := s.EndsAt.Sub(s.StartsAt); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("silence window = %s, want ~30m", d)
	}
	if s.EndsAt.Before(before) {
		t.Errorf("silence already expired at creation: endsAt=%s", s.EndsAt)
	}
	if s.CreatedBy == "" || s.Comment == "" {
		t.Errorf("silence must be attributable: createdBy=%q comment=%q", s.CreatedBy, s.Comment)
	}
}

// A skipped item was never dispatched — silencing it would hide an alert
// nobody is looking at.
func TestAcknowledgeSkipDoesNotSilence(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	item := pollOne(t, a)
	if err := a.Acknowledge(context.Background(), item, model.AckActionSkip); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if got := srv.silences(); len(got) != 0 {
		t.Fatalf("expected no silence for a skip, got %d", len(got))
	}
}

// Re-acknowledging the same fire cycle must not stack a second silence.
func TestAcknowledgeIsIdempotentPerFireCycle(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	item := pollOne(t, a)
	for i := 0; i < 3; i++ {
		if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
			t.Fatalf("Acknowledge #%d: %v", i+1, err)
		}
	}
	if got := srv.silences(); len(got) != 1 {
		t.Fatalf("expected exactly 1 silence across 3 acks, got %d", len(got))
	}
}

// After a restart the poll-time label cache is cold; the item's "key:value"
// labels must still reproduce the exact matcher set.
func TestAcknowledgeFallsBackToItemLabels(t *testing.T) {
	alerts := []map[string]any{}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	// Never polled: this item came back from a persisted binding.
	item := model.SourceItem{
		ID:       "abcdef1234567890:2026-01-01T00:00:00Z",
		SourceID: "prod-alerts",
		Labels:   []string{"alertname:HighErrorRate", "instance:web-1:9100", "severity:critical"},
	}
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	got := srv.silences()
	if len(got) != 1 {
		t.Fatalf("expected 1 silence, got %d", len(got))
	}
	m := matcherMap(t, got[0])
	want := map[string]string{"alertname": "HighErrorRate", "instance": "web-1:9100", "severity": "critical"}
	if len(m) != len(want) {
		t.Fatalf("matchers = %v, want %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("matcher %s = %q, want %q — a colon in a label value must survive", k, m[k], v)
		}
	}
}

// With no labels at all a silence would match every alert. Refuse to create it.
func TestAcknowledgeWithoutLabelsDoesNotSilence(t *testing.T) {
	alerts := []map[string]any{}
	srv := newSilenceServer(t, &alerts)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	item := model.SourceItem{ID: "unknown:2026-01-01T00:00:00Z", SourceID: "prod-alerts"}
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge must not fail the dispatch: %v", err)
	}
	if got := srv.silences(); len(got) != 0 {
		t.Fatalf("expected no silence without labels, got %d", len(got))
	}
}

// A rejected silence surfaces as an error so the failure is visible in the log
// rather than silently leaving the alert paging.
func TestAcknowledgeReportsSilenceFailure(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	srv.rejectWith(http.StatusForbidden)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	item := pollOne(t, a)
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err == nil {
		t.Fatal("expected an error when Alertmanager rejects the silence")
	}
}

// A failed silence must not be recorded as done — the next ack retries.
func TestAcknowledgeRetriesAfterFailure(t *testing.T) {
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", time.Now().Add(-10*time.Minute))}
	srv := newSilenceServer(t, &alerts)
	srv.rejectWith(http.StatusForbidden)
	a := connect(t, srv.URL, map[string]any{"ack_via_silence": true})

	item := pollOne(t, a)
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err == nil {
		t.Fatal("expected the first ack to fail")
	}

	srv.rejectWith(0)
	if err := a.Acknowledge(context.Background(), item, model.AckActionInProgress); err != nil {
		t.Fatalf("second Acknowledge: %v", err)
	}
	if got := srv.silences(); len(got) != 1 {
		t.Fatalf("expected the retry to create the silence, got %d", len(got))
	}
}

func TestConnectRejectsBadSilenceDuration(t *testing.T) {
	for _, v := range []any{"nope", "0s", "-5m", 42} {
		a := &Adapter{}
		err := a.Connect(context.Background(), map[string]any{
			"alertmanager_url": "http://localhost:9093",
			"silence_duration": v,
		})
		if err == nil {
			t.Errorf("silence_duration=%v: expected an error", v)
		}
	}
}
