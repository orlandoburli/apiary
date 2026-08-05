package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// newTestServer serves the given alerts on GET /api/v2/alerts and records the
// last request's query values.
func newTestServer(t *testing.T, alerts *[]map[string]any, lastQuery *map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		if lastQuery != nil {
			*lastQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(*alerts)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mkAlert(fingerprint, name, severity string, startsAt time.Time) map[string]any {
	return map[string]any{
		"fingerprint": fingerprint,
		"labels":      map[string]string{"alertname": name, "severity": severity, "team": "platform"},
		"annotations": map[string]string{
			"summary":     "error rate above 5%",
			"description": "the service is returning 5xx",
			"runbook_url": "https://runbooks.internal/high-error-rate",
		},
		"startsAt":     startsAt.UTC().Format(time.RFC3339),
		"updatedAt":    startsAt.Add(time.Minute).UTC().Format(time.RFC3339),
		"generatorURL": "https://prometheus.internal/graph?g0.expr=rate",
		"status":       map[string]any{"state": "active"},
	}
}

func connect(t *testing.T, url string, extra map[string]any) *Adapter {
	t.Helper()
	a := &Adapter{}
	a.SetID("prod-alerts")
	cfg := map[string]any{"alertmanager_url": url}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := a.Connect(context.Background(), cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return a
}

func TestConnectRequiresURL(t *testing.T) {
	a := &Adapter{}
	if err := a.Connect(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected error for missing alertmanager_url")
	}
}

func TestPollMapsAlerts(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	alerts := []map[string]any{mkAlert("abcdef1234567890", "HighErrorRate", "critical", started)}
	srv := newTestServer(t, &alerts, nil)
	a := connect(t, srv.URL, nil)

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]

	wantID := "abcdef1234567890:" + started.UTC().Format(time.RFC3339)
	if it.ID != wantID {
		t.Errorf("ID = %q, want %q", it.ID, wantID)
	}
	if it.SourceID != "prod-alerts" {
		t.Errorf("SourceID = %q", it.SourceID)
	}
	if it.Number != "abcdef1" {
		t.Errorf("Number = %q, want short fingerprint", it.Number)
	}
	if it.Title != "HighErrorRate: error rate above 5%" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.State != "firing" {
		t.Errorf("State = %q, want firing", it.State)
	}
	if it.Type != "alert" {
		t.Errorf("Type = %q, want alert", it.Type)
	}
	if it.Priority != "critical" {
		t.Errorf("Priority = %q, want critical", it.Priority)
	}
	wantLabels := []string{"alertname:HighErrorRate", "severity:critical", "team:platform"}
	if len(it.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", it.Labels, wantLabels)
	}
	for i, l := range wantLabels {
		if it.Labels[i] != l {
			t.Errorf("Labels[%d] = %q, want %q", i, it.Labels[i], l)
		}
	}
	if it.Metadata["fingerprint"] != "abcdef1234567890" {
		t.Errorf("Metadata.fingerprint = %v", it.Metadata["fingerprint"])
	}
	for _, want := range []string{"error rate above 5%", "the service is returning 5xx", "`severity`: `critical`", "runbook_url", "Firing since"} {
		if !strings.Contains(it.Description, want) {
			t.Errorf("Description missing %q:\n%s", want, it.Description)
		}
	}
}

func TestPollIDStableAcrossPolls(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	alerts := []map[string]any{mkAlert("f1", "HighErrorRate", "critical", started)}
	srv := newTestServer(t, &alerts, nil)
	a := connect(t, srv.URL, nil)

	first, _ := a.Poll(context.Background(), time.Time{})
	second, _ := a.Poll(context.Background(), time.Time{})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected the firing alert on every poll: %d then %d", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("ID changed across polls: %q vs %q", first[0].ID, second[0].ID)
	}

	// A re-fire after resolution (new startsAt) is a NEW item.
	alerts[0] = mkAlert("f1", "HighErrorRate", "critical", started.Add(30*time.Minute))
	third, _ := a.Poll(context.Background(), time.Time{})
	if len(third) != 1 {
		t.Fatalf("expected 1 item on re-fire, got %d", len(third))
	}
	if third[0].ID == first[0].ID {
		t.Error("re-fired alert kept the old ID — would never re-dispatch")
	}
}

func TestPollMinAgeDampensFlaps(t *testing.T) {
	alerts := []map[string]any{
		mkAlert("young", "Flappy", "warning", time.Now().Add(-10*time.Second)),
		mkAlert("old", "Stable", "critical", time.Now().Add(-10*time.Minute)),
	}
	srv := newTestServer(t, &alerts, nil)
	a := connect(t, srv.URL, map[string]any{"min_age": "1m"})

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 || items[0].Metadata["fingerprint"] != "old" {
		t.Fatalf("expected only the old alert, got %+v", items)
	}
}

func TestPollStormCap(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	var alerts []map[string]any
	for i := 0; i < 5; i++ {
		alerts = append(alerts, mkAlert(fmt.Sprintf("f%d", i), fmt.Sprintf("Alert%d", i), "critical",
			started.Add(time.Duration(i)*time.Minute)))
	}
	srv := newTestServer(t, &alerts, nil)
	a := connect(t, srv.URL, map[string]any{"max_new_per_poll": 2})

	// Poll 1: only the 2 oldest new alerts surface.
	items, _ := a.Poll(context.Background(), time.Time{})
	if len(items) != 2 {
		t.Fatalf("poll 1: expected 2 items, got %d", len(items))
	}
	if items[0].Metadata["fingerprint"] != "f0" || items[1].Metadata["fingerprint"] != "f1" {
		t.Errorf("poll 1: expected oldest first, got %v and %v", items[0].Metadata["fingerprint"], items[1].Metadata["fingerprint"])
	}

	// Poll 2: the 2 already-seen keep flowing plus 2 more new ones.
	items, _ = a.Poll(context.Background(), time.Time{})
	if len(items) != 4 {
		t.Fatalf("poll 2: expected 4 items, got %d", len(items))
	}

	// Poll 3: all 5.
	items, _ = a.Poll(context.Background(), time.Time{})
	if len(items) != 5 {
		t.Fatalf("poll 3: expected 5 items, got %d", len(items))
	}
}

func TestPollSendsFilters(t *testing.T) {
	alerts := []map[string]any{}
	var q map[string][]string
	srv := newTestServer(t, &alerts, &q)
	a := connect(t, srv.URL, nil)
	a.SetFilters([]string{"firing"}, []string{"severity=critical", "team:platform", `env=~"prod|staging"`})

	if _, err := a.Poll(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	want := []string{`severity="critical"`, `team="platform"`, `env=~"prod|staging"`}
	got := q["filter"]
	if len(got) != len(want) {
		t.Fatalf("filter params = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filter[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if v := q["active"]; len(v) != 1 || v[0] != "true" {
		t.Errorf("active param = %v, want [true]", v)
	}
	if v := q["silenced"]; len(v) != 1 || v[0] != "false" {
		t.Errorf("silenced param = %v, want [false]", v)
	}
}

func TestWriteOpsAreNoOps(t *testing.T) {
	a := &Adapter{}
	if err := a.Acknowledge(context.Background(), itemStub(), "in_progress"); err != nil {
		t.Errorf("Acknowledge: %v", err)
	}
	if err := a.WriteResult(context.Background(), itemStub(), resultStub()); err != nil {
		t.Errorf("WriteResult: %v", err)
	}
	if a.WebhookHandler() != nil {
		t.Error("WebhookHandler should be nil for the poll-only adapter")
	}
}

func TestToMatcher(t *testing.T) {
	cases := map[string]string{
		"severity=critical":   `severity="critical"`,
		"severity:critical":   `severity="critical"`,
		" team = platform ":   `team="platform"`,
		`env=~"prod|staging"`: `env=~"prod|staging"`,
		`env!="dev"`:          `env!="dev"`,
		"HighErrorRate":       `alertname="HighErrorRate"`,
	}
	for in, want := range cases {
		if got := toMatcher(in); got != want {
			t.Errorf("toMatcher(%q) = %q, want %q", in, got, want)
		}
	}
}

func itemStub() model.SourceItem {
	return model.SourceItem{ID: "f1:2026-08-05T00:00:00Z", SourceID: "prod-alerts"}
}

func resultStub() model.RunResult {
	return model.RunResult{Success: true}
}
