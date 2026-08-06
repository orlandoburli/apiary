package webhook

import (
	"strings"
	"testing"
	"time"
)

func genericAdapter(t *testing.T) *Adapter {
	t.Helper()
	return newAdapter(t, map[string]any{"secret": "tok"})
}

func TestGenericSingleObject(t *testing.T) {
	a := genericAdapter(t)
	items, err := a.parse([]byte(`{
		"id": "deploy-42",
		"title": "Deploy failed",
		"description": "the rollout stalled",
		"labels": {"severity": "critical", "service": "api"},
		"severity": "critical",
		"state": "failing",
		"url": "https://ci.example.com/42"
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.ID != "deploy-42" || it.Title != "Deploy failed" || it.State != "failing" || it.Priority != "critical" || it.URL != "https://ci.example.com/42" {
		t.Errorf("mapped item wrong: %+v", it)
	}
	if len(it.Labels) != 2 || it.Labels[0] != "service:api" || it.Labels[1] != "severity:critical" {
		t.Errorf("labels = %v, want sorted k:v pairs", it.Labels)
	}
	if !strings.Contains(it.Description, "the rollout stalled") || !strings.Contains(it.Description, "```json") {
		t.Errorf("description missing content or payload block:\n%s", it.Description)
	}
	if it.Type != "webhook" {
		t.Errorf("type = %q, want webhook", it.Type)
	}
}

func TestGenericFallbacks(t *testing.T) {
	a := genericAdapter(t)
	body := `{"summary":"disk almost full","labels":["team=sre","critical"]}`
	items, err := a.parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	it := items[0]
	if it.Title != "disk almost full" {
		t.Errorf("title fallback to summary failed: %q", it.Title)
	}
	if it.ID == "" || len(it.ID) != 16 {
		t.Errorf("hash id = %q, want 16 hex chars", it.ID)
	}
	if it.State != "open" {
		t.Errorf("state default = %q, want open", it.State)
	}
	if len(it.Labels) != 2 || it.Labels[0] != "critical" || it.Labels[1] != "team:sre" {
		t.Errorf("array labels = %v", it.Labels)
	}

	// Identical body → identical hash id; different body → different id.
	again, _ := a.parse([]byte(body))
	if again[0].ID != it.ID {
		t.Error("hash id is not stable for the same body")
	}
	other, _ := a.parse([]byte(`{"summary":"something else"}`))
	if other[0].ID == it.ID {
		t.Error("distinct bodies must hash to distinct ids")
	}
}

func TestGenericBatches(t *testing.T) {
	a := genericAdapter(t)

	arr, err := a.parse([]byte(`[{"id":"a"},{"id":"b"}]`))
	if err != nil || len(arr) != 2 {
		t.Fatalf("array batch: %v (%d items)", err, len(arr))
	}
	env, err := a.parse([]byte(`{"events":[{"id":"a"},{"id":"b"},{"id":"c"}]}`))
	if err != nil || len(env) != 3 {
		t.Fatalf("events envelope: %v (%d items)", err, len(env))
	}
}

func TestAlertmanagerFormat(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok", "format": "alertmanager"})

	payload := `{
		"version": "4",
		"status": "firing",
		"alerts": [
			{
				"status": "firing",
				"labels": {"alertname": "HighErrorRate", "severity": "critical", "service": "api"},
				"annotations": {"summary": "5xx over 5%", "description": "error budget burning"},
				"startsAt": "2026-08-05T10:00:00Z",
				"endsAt": "0001-01-01T00:00:00Z",
				"generatorURL": "http://prom/graph?g0.expr=...",
				"fingerprint": "abcdef1234567890"
			},
			{
				"status": "resolved",
				"labels": {"alertname": "OldAlert"},
				"startsAt": "2026-08-05T08:00:00Z",
				"fingerprint": "ffff000011112222"
			},
			{
				"status": "firing",
				"labels": {"alertname": "NoFingerprint"},
				"startsAt": "2026-08-05T09:00:00Z"
			}
		]
	}`
	items, err := a.parse([]byte(payload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (resolved + fingerprint-less skipped)", len(items))
	}
	it := items[0]
	if it.ID != "abcdef1234567890:2026-08-05T10:00:00Z" {
		t.Errorf("id = %q, want fingerprint:startsAt", it.ID)
	}
	if it.Title != "HighErrorRate: 5xx over 5%" {
		t.Errorf("title = %q", it.Title)
	}
	if it.State != "firing" || it.Type != "alert" || it.Priority != "critical" {
		t.Errorf("state/type/priority wrong: %+v", it)
	}
	if it.Number != "abcdef1" {
		t.Errorf("number = %q, want abcdef1", it.Number)
	}
	want := []string{"alertname:HighErrorRate", "service:api", "severity:critical"}
	if len(it.Labels) != 3 || it.Labels[0] != want[0] || it.Labels[1] != want[1] || it.Labels[2] != want[2] {
		t.Errorf("labels = %v, want %v", it.Labels, want)
	}
	if !it.CreatedAt.Equal(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("createdAt = %v", it.CreatedAt)
	}
	if !strings.Contains(it.Description, "error budget burning") || !strings.Contains(it.Description, "`severity`: `critical`") {
		t.Errorf("description missing annotation/labels:\n%s", it.Description)
	}
}

func TestAlertmanagerInvalid(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok", "format": "alertmanager"})
	if _, err := a.parse([]byte(`{"version":"4"}`)); err == nil {
		t.Error("payload without alerts array should fail")
	}
	if _, err := a.parse([]byte(`not json`)); err == nil {
		t.Error("non-JSON should fail")
	}
}
