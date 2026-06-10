package cursorusage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Fixture mirrors live get-filtered-usage-events responses: protobuf-es
// camelCase keys, stringified timestamps, costs in fractional cents on
// chargedCents/totalCents and a "$x.xx" display string.
const eventJSON = `{
	"timestamp": "1765360800000",
	"model": "claude-4.5-sonnet",
	"kind": "USAGE_EVENT_KIND_USAGE_BASED",
	"maxMode": false,
	"requestsCosts": 30.4,
	"usageBasedCosts": "$1.21",
	"isTokenBasedCall": true,
	"tokenUsage": {
		"inputTokens": 3,
		"outputTokens": 20525,
		"cacheWriteTokens": 112151,
		"cacheReadTokens": 0,
		"totalCents": 121.41
	},
	"cursorTokenFee": 3.32,
	"isChargeable": true,
	"isHeadless": true,
	"chargedCents": 124.73
}`

func TestFetchEvents(t *testing.T) {
	var gotOrigin, gotCookie string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin = r.Header.Get("Origin")
		if c, err := r.Cookie("WorkosCursorSessionToken"); err == nil {
			gotCookie = c.Value
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprintf(w, `{"totalUsageEventsCount":1,"usageEventsDisplay":[%s]}`, eventJSON)
	}))
	defer srv.Close()

	c := &Client{Token: "user_123%3A%3Ajwt", BaseURL: srv.URL}
	events, err := c.FetchEvents(context.Background(), time.UnixMilli(1765357200000), time.UnixMilli(1765364400000))
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if gotOrigin != "https://cursor.com" {
		t.Errorf("Origin = %q, want https://cursor.com (API rejects other origins)", gotOrigin)
	}
	if gotCookie != "user_123%3A%3Ajwt" {
		t.Errorf("cookie = %q, want token passed through", gotCookie)
	}
	if gotBody["teamId"] != float64(0) || gotBody["pageSize"] != float64(100) {
		t.Errorf("body = %v, want teamId 0 and pageSize 100", gotBody)
	}
	if gotBody["startDate"] != "1765357200000" {
		t.Errorf("startDate = %v, want stringified epoch ms", gotBody["startDate"])
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Model != "claude-4.5-sonnet" || ev.Kind != "USAGE_EVENT_KIND_USAGE_BASED" {
		t.Errorf("event = %+v", ev)
	}
	if ev.TokenUsage == nil || ev.TokenUsage.CacheWriteTokens != 112151 {
		t.Errorf("tokenUsage = %+v, want cacheWriteTokens 112151", ev.TokenUsage)
	}
	if ev.IsHeadless == nil || !*ev.IsHeadless {
		t.Errorf("isHeadless = %v, want true", ev.IsHeadless)
	}
	if got := ev.Time(); !got.Equal(time.UnixMilli(1765360800000)) {
		t.Errorf("Time() = %v", got)
	}
}

func TestFetchEventsPagination(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		pages++
		n := pageSize
		if body["page"] == float64(2) {
			n = 3 // short page ends the walk
		}
		evs := make([]json.RawMessage, n)
		for i := range evs {
			evs[i] = json.RawMessage(eventJSON)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalUsageEventsCount": pageSize + 3,
			"usageEventsDisplay":    evs,
		})
	}))
	defer srv.Close()

	c := &Client{Token: "t", BaseURL: srv.URL}
	events, err := c.FetchEvents(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if pages != 2 || len(events) != pageSize+3 {
		t.Errorf("pages = %d, events = %d; want 2 pages, %d events", pages, len(events), pageSize+3)
	}
}

func TestFetchEventsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Client{Token: "expired", BaseURL: srv.URL}
	if _, err := c.FetchEvents(context.Background(), time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Fatal("want auth error, got nil")
	}
}

func TestCostUSD(t *testing.T) {
	headless := true
	cases := []struct {
		name string
		ev   UsageEvent
		want float64
	}{
		{"chargedCents preferred", UsageEvent{Kind: "USAGE_EVENT_KIND_USAGE_BASED", ChargedCents: 124.73, TokenUsage: &TokenUsage{TotalCents: 121.41}, CursorTokenFee: 3.32, IsHeadless: &headless}, 1.2473},
		{"totalCents plus fee fallback", UsageEvent{Kind: "USAGE_EVENT_KIND_USAGE_BASED", TokenUsage: &TokenUsage{TotalCents: 121.41}, CursorTokenFee: 3.32}, 1.2473},
		{"display string fallback", UsageEvent{Kind: "USAGE_EVENT_KIND_USAGE_BASED", UsageBasedCosts: "$1.21"}, 1.21},
		{"dash display is free", UsageEvent{Kind: "USAGE_EVENT_KIND_INCLUDED_IN_PRO", UsageBasedCosts: "-"}, 0},
		{"errored never charged", UsageEvent{Kind: "USAGE_EVENT_KIND_ERRORED_NOT_CHARGED", ChargedCents: 50}, 0},
		{"aborted never charged", UsageEvent{Kind: "USAGE_EVENT_KIND_ABORTED_NOT_CHARGED", UsageBasedCosts: "$0.50"}, 0},
	}
	for _, tc := range cases {
		if got := tc.ev.CostUSD(); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: CostUSD = %v, want %v", tc.name, got, tc.want)
		}
	}
}
