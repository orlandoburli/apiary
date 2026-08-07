package prometheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// resolveServer serves an alert listing and records the query it was asked
// with, so tests can assert the resolution check widens its view to include
// suppressed alerts.
type resolveServer struct {
	*httptest.Server

	mu        sync.Mutex
	alerts    []map[string]any
	lastQuery map[string][]string
	failWith  int
	calls     int
}

func newResolveServer(t *testing.T) *resolveServer {
	t.Helper()
	s := &resolveServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.calls++
		s.lastQuery = r.URL.Query()
		fail, alerts := s.failWith, s.alerts
		s.mu.Unlock()

		if fail != 0 {
			http.Error(w, "boom", fail)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(alerts)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *resolveServer) setAlerts(a []map[string]any) {
	s.mu.Lock()
	s.alerts = a
	s.mu.Unlock()
}

func (s *resolveServer) setFailure(status int) {
	s.mu.Lock()
	s.failWith = status
	s.mu.Unlock()
}

func (s *resolveServer) query() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastQuery
}

// resolveTwice runs the two consecutive checks the debounce requires and
// returns what the second one reported.
func resolveTwice(t *testing.T, a *Adapter, ids []string) []string {
	t.Helper()
	if _, err := a.ResolvedItems(context.Background(), ids); err != nil {
		t.Fatalf("ResolvedItems (first): %v", err)
	}
	got, err := a.ResolvedItems(context.Background(), ids)
	if err != nil {
		t.Fatalf("ResolvedItems (second): %v", err)
	}
	return got
}

// The resolution check must ask for silenced and inhibited alerts too —
// otherwise a suppressed alert reads as resolved.
func TestResolvedItemsQueriesSuppressedAlerts(t *testing.T) {
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	if _, err := a.ResolvedItems(context.Background(), []string{"abc:2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	q := srv.query()
	for _, key := range []string{"silenced", "inhibited"} {
		var got string
		if vs := q[key]; len(vs) > 0 {
			got = vs[0]
		}
		if got != "true" {
			t.Errorf("query %s = %q, want \"true\" — a suppressed alert is not a resolved alert", key, got)
		}
	}
}

// An alert Apiary silenced itself (ack_via_silence) is still firing.
func TestResolvedItemsIgnoresSilencedAlert(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	al := mkAlert("abcdef1234567890", "HighErrorRate", "critical", started)
	al["status"] = map[string]any{"state": "suppressed", "silencedBy": []string{"sil-1"}}

	srv := newResolveServer(t)
	srv.setAlerts([]map[string]any{al})
	a := connect(t, srv.URL, nil)

	id := "abcdef1234567890:" + started.UTC().Format(time.RFC3339)
	if got := resolveTwice(t, a, []string{id}); len(got) != 0 {
		t.Fatalf("silenced alert reported resolved: %v", got)
	}
}

// An alert that vanished from Alertmanager is resolved — after confirmation.
func TestResolvedItemsReportsMissingAlert(t *testing.T) {
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	id := "abcdef1234567890:2026-01-01T00:00:00Z"
	got := resolveTwice(t, a, []string{id})
	if len(got) != 1 || got[0] != id {
		t.Fatalf("ResolvedItems = %v, want [%s]", got, id)
	}
}

// One check is never enough: a single empty listing must not interrupt work.
func TestResolvedItemsNeedsTwoConfirmations(t *testing.T) {
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	id := "abcdef1234567890:2026-01-01T00:00:00Z"
	got, err := a.ResolvedItems(context.Background(), []string{id})
	if err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("reported resolved on the first check: %v", got)
	}
}

// An alert that blips out of the listing and comes back must reset the streak,
// not accumulate toward an interrupt.
func TestResolvedItemsResetsStreakWhenAlertReturns(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	id := "abcdef1234567890:" + started.UTC().Format(time.RFC3339)
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	// Check 1: missing.
	if _, err := a.ResolvedItems(context.Background(), []string{id}); err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	// Check 2: back again — this must clear the streak.
	srv.setAlerts([]map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", started)})
	if got, err := a.ResolvedItems(context.Background(), []string{id}); err != nil || len(got) != 0 {
		t.Fatalf("ResolvedItems = %v, err = %v; want none", got, err)
	}
	// Check 3: missing again — one confirmation, not two.
	srv.setAlerts(nil)
	got, err := a.ResolvedItems(context.Background(), []string{id})
	if err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("streak was not reset by the alert reappearing: %v", got)
	}
}

// A resolved alert lingers with endsAt in the past until resolve_timeout.
func TestResolvedItemsHonoursEndsAtInThePast(t *testing.T) {
	started := time.Now().Add(-30 * time.Minute)
	al := mkAlert("abcdef1234567890", "HighErrorRate", "critical", started)
	al["endsAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)

	srv := newResolveServer(t)
	srv.setAlerts([]map[string]any{al})
	a := connect(t, srv.URL, nil)

	id := "abcdef1234567890:" + started.UTC().Format(time.RFC3339)
	got := resolveTwice(t, a, []string{id})
	if len(got) != 1 || got[0] != id {
		t.Fatalf("ResolvedItems = %v, want [%s] — endsAt in the past means resolved", got, id)
	}
}

// A still-firing alert carries endsAt in the future; it is not resolved.
func TestResolvedItemsKeepsAlertWithFutureEndsAt(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	al := mkAlert("abcdef1234567890", "HighErrorRate", "critical", started)
	al["endsAt"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	srv := newResolveServer(t)
	srv.setAlerts([]map[string]any{al})
	a := connect(t, srv.URL, nil)

	id := "abcdef1234567890:" + started.UTC().Format(time.RFC3339)
	if got := resolveTwice(t, a, []string{id}); len(got) != 0 {
		t.Fatalf("firing alert reported resolved: %v", got)
	}
}

// An unreachable Alertmanager means "could not tell", never "resolved".
func TestResolvedItemsFailsClosedOnError(t *testing.T) {
	srv := newResolveServer(t)
	srv.setFailure(http.StatusInternalServerError)
	a := connect(t, srv.URL, nil)

	got, err := a.ResolvedItems(context.Background(), []string{"abc:2026-01-01T00:00:00Z"})
	if err == nil {
		t.Fatal("expected an error when Alertmanager is unreachable")
	}
	if len(got) != 0 {
		t.Fatalf("reported %v resolved despite the error", got)
	}
}

// No in-flight items means no API call at all.
func TestResolvedItemsSkipsEmptyInput(t *testing.T) {
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	got, err := a.ResolvedItems(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("ResolvedItems(nil) = %v, %v; want nil, nil", got, err)
	}
	srv.mu.Lock()
	calls := srv.calls
	srv.mu.Unlock()
	if calls != 0 {
		t.Fatalf("made %d API call(s) for an empty item set", calls)
	}
}

// The streak map must not accumulate ids that are no longer in flight.
func TestResolvedItemsForgetsUntrackedItems(t *testing.T) {
	srv := newResolveServer(t)
	a := connect(t, srv.URL, nil)

	if _, err := a.ResolvedItems(context.Background(), []string{"gone:1", "gone:2"}); err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	if _, err := a.ResolvedItems(context.Background(), []string{"gone:1"}); err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}

	a.mu.Lock()
	_, stillTracked := a.resolveStreak["gone:2"]
	a.mu.Unlock()
	if stillTracked {
		t.Error("resolveStreak still tracks an item that is no longer in flight")
	}
}

// The adapter must satisfy the capability the dispatcher probes for.
func TestAdapterImplementsItemResolver(t *testing.T) {
	var a any = &Adapter{}
	if _, ok := a.(interface {
		ResolvedItems(context.Context, []string) ([]string, error)
	}); !ok {
		t.Fatal("*Adapter does not implement source.ItemResolver")
	}
}
