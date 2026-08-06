package dynatrace

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

// newTestServer serves the given problems on GET /api/v2/problems and records
// the last request's query values.
func newTestServer(t *testing.T, problems *[]map[string]any, lastQuery *map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/problems" {
			http.NotFound(w, r)
			return
		}
		if lastQuery != nil {
			*lastQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"totalCount": len(*problems),
			"pageSize":   500,
			"problems":   *problems,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mkProblem(id, displayID, title string, start time.Time) map[string]any {
	return map[string]any{
		"problemId":     id,
		"displayId":     displayID,
		"title":         title,
		"impactLevel":   "SERVICES",
		"severityLevel": "AVAILABILITY",
		"status":        "OPEN",
		"startTime":     start.UnixMilli(),
		"endTime":       -1,
		"affectedEntities": []map[string]any{
			{"entityId": map[string]string{"id": "SERVICE-1", "type": "SERVICE"}, "name": "checkout-service"},
		},
		"rootCauseEntity": map[string]any{
			"entityId": map[string]string{"id": "HOST-1", "type": "HOST"}, "name": "prod-host-7",
		},
		"managementZones": []map[string]any{{"id": "z1", "name": "Prod"}},
		"entityTags": []map[string]any{
			{"context": "CONTEXTLESS", "key": "team", "value": "platform", "stringRepresentation": "team:platform"},
		},
	}
}

func connect(t *testing.T, url string, extra map[string]any) *Adapter {
	t.Helper()
	a := &Adapter{}
	a.SetID("prod-problems")
	cfg := map[string]any{"base_url": url, "api_token": "dt0c01.sekret"}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := a.Connect(context.Background(), cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return a
}

func TestConnectRequiresURLAndToken(t *testing.T) {
	a := &Adapter{}
	if err := a.Connect(context.Background(), map[string]any{"api_token": "x"}); err == nil {
		t.Fatal("expected error for missing base_url")
	}
	if err := a.Connect(context.Background(), map[string]any{"base_url": "https://abc.live.dynatrace.com"}); err == nil {
		t.Fatal("expected error for missing api_token")
	}
}

func TestPollMapsProblems(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	problems := []map[string]any{mkProblem("6120279862528180338_1620308532000V2", "P-2145", "Response time degradation", started)}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, nil)

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]

	if it.ID != "6120279862528180338_1620308532000V2" {
		t.Errorf("ID = %q, want the problemId", it.ID)
	}
	if it.SourceID != "prod-problems" {
		t.Errorf("SourceID = %q", it.SourceID)
	}
	if it.Number != "P-2145" {
		t.Errorf("Number = %q, want the displayId", it.Number)
	}
	if it.Title != "Response time degradation" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.State != "open" {
		t.Errorf("State = %q, want open", it.State)
	}
	if it.Type != "problem" {
		t.Errorf("Type = %q, want problem", it.Type)
	}
	if it.Priority != "availability" {
		t.Errorf("Priority = %q, want availability", it.Priority)
	}
	wantLabels := []string{"impact:services", "severity:availability", "team:platform", "zone:Prod"}
	if len(it.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", it.Labels, wantLabels)
	}
	for i, l := range wantLabels {
		if it.Labels[i] != l {
			t.Errorf("Labels[%d] = %q, want %q", i, it.Labels[i], l)
		}
	}
	wantURL := srv.URL + "/#problems/problemdetails;pid=6120279862528180338_1620308532000V2"
	if it.URL != wantURL {
		t.Errorf("URL = %q, want %q", it.URL, wantURL)
	}
	if it.Metadata["displayId"] != "P-2145" {
		t.Errorf("Metadata.displayId = %v", it.Metadata["displayId"])
	}
	for _, want := range []string{"P-2145", "AVAILABILITY", "checkout-service", "prod-host-7", "Prod", "team:platform", "Open since"} {
		if !strings.Contains(it.Description, want) {
			t.Errorf("Description missing %q:\n%s", want, it.Description)
		}
	}
}

func TestPollIDStableAcrossPolls(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	problems := []map[string]any{mkProblem("pid-1", "P-1", "Down", started)}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, nil)

	first, _ := a.Poll(context.Background(), time.Time{})
	second, _ := a.Poll(context.Background(), time.Time{})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected the open problem on every poll: %d then %d", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("ID changed across polls: %q vs %q", first[0].ID, second[0].ID)
	}

	// A new occurrence after resolution (fresh problemId) is a NEW item.
	problems[0] = mkProblem("pid-2", "P-2", "Down", started.Add(30*time.Minute))
	third, _ := a.Poll(context.Background(), time.Time{})
	if len(third) != 1 {
		t.Fatalf("expected 1 item on re-occurrence, got %d", len(third))
	}
	if third[0].ID == first[0].ID {
		t.Error("new problem occurrence kept the old ID — would never re-dispatch")
	}
}

func TestPollMinAgeDampensFlaps(t *testing.T) {
	problems := []map[string]any{
		mkProblem("young", "P-1", "Flappy", time.Now().Add(-10*time.Second)),
		mkProblem("old", "P-2", "Stable", time.Now().Add(-10*time.Minute)),
	}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, map[string]any{"min_age": "1m"})

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 || items[0].Metadata["problemId"] != "old" {
		t.Fatalf("expected only the old problem, got %+v", items)
	}
}

func TestPollStormCap(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	var problems []map[string]any
	for i := 0; i < 5; i++ {
		problems = append(problems, mkProblem(fmt.Sprintf("pid-%d", i), fmt.Sprintf("P-%d", i), fmt.Sprintf("Problem %d", i),
			started.Add(time.Duration(i)*time.Minute)))
	}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, map[string]any{"max_new_per_poll": 2})

	// Poll 1: only the 2 oldest new problems surface.
	items, _ := a.Poll(context.Background(), time.Time{})
	if len(items) != 2 {
		t.Fatalf("poll 1: expected 2 items, got %d", len(items))
	}
	if items[0].Metadata["problemId"] != "pid-0" || items[1].Metadata["problemId"] != "pid-1" {
		t.Errorf("poll 1: expected oldest first, got %v and %v", items[0].Metadata["problemId"], items[1].Metadata["problemId"])
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

func TestPollSendsSelectorAndWindow(t *testing.T) {
	problems := []map[string]any{}
	var q map[string][]string
	srv := newTestServer(t, &problems, &q)
	a := connect(t, srv.URL, nil)
	a.SetFilters([]string{"open"}, []string{"severity=availability", "team:platform", `managementZones("Prod")`, "zone=Web", "checkout"})

	if _, err := a.Poll(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	want := `status("open"),severityLevel("AVAILABILITY"),entityTags("team:platform"),managementZones("Prod"),managementZones("Web"),text("checkout")`
	if got := q["problemSelector"]; len(got) != 1 || got[0] != want {
		t.Errorf("problemSelector = %v, want %q", q["problemSelector"], want)
	}
	if got := q["from"]; len(got) != 1 || got[0] == "" {
		t.Errorf("from param = %v, want an explicit lookback window", q["from"])
	}
	if got := q["pageSize"]; len(got) != 1 || got[0] != "500" {
		t.Errorf("pageSize param = %v, want [500]", q["pageSize"])
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

func TestToCriterion(t *testing.T) {
	cases := map[string]string{
		"severity=availability":         `severityLevel("AVAILABILITY")`,
		"severityLevel:ERROR":           `severityLevel("ERROR")`,
		"impact=services":               `impactLevel("SERVICES")`,
		"managementZone=Prod":           `managementZones("Prod")`,
		" zone = Web ":                  `managementZones("Web")`,
		"team=platform":                 `entityTags("team:platform")`,
		"team:platform":                 `entityTags("team:platform")`,
		`displayId("P-123")`:            `displayId("P-123")`,
		`severityLevel("CUSTOM_ALERT")`: `severityLevel("CUSTOM_ALERT")`,
		"checkout":                      `text("checkout")`,
	}
	for in, want := range cases {
		if got := toCriterion(in); got != want {
			t.Errorf("toCriterion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConnectRejectsInvalidConfig(t *testing.T) {
	cases := map[string]map[string]any{
		"non-numeric max_new_per_poll": {"max_new_per_poll": "ten"},
		"negative max_new_per_poll":    {"max_new_per_poll": -1},
		"unparseable min_age":          {"min_age": "soon"},
		"negative min_age":             {"min_age": "-1m"},
		"non-string min_age":           {"min_age": 5},
		"unparseable lookback":         {"lookback": "forever"},
		"zero lookback":                {"lookback": "0h"},
	}
	for name, extra := range cases {
		cfg := map[string]any{"base_url": "https://abc.live.dynatrace.com", "api_token": "x"}
		for k, v := range extra {
			cfg[k] = v
		}
		a := &Adapter{}
		if err := a.Connect(context.Background(), cfg); err == nil {
			t.Errorf("%s: expected Connect error for %v", name, extra)
		}
	}
}

func TestPollSkipsProblemsWithoutID(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	broken := mkProblem("", "P-0", "NoID", started)
	problems := []map[string]any{broken, mkProblem("ok", "P-1", "Fine", started)}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, nil)

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 || items[0].Metadata["problemId"] != "ok" {
		t.Fatalf("expected only the identified problem, got %+v", items)
	}
}

func TestPollStormCapZeroMeansUnlimited(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	var problems []map[string]any
	for i := 0; i < 25; i++ {
		problems = append(problems, mkProblem(fmt.Sprintf("pid-%d", i), fmt.Sprintf("P-%d", i), fmt.Sprintf("Problem %d", i), started))
	}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, map[string]any{"max_new_per_poll": 0})

	items, _ := a.Poll(context.Background(), time.Time{})
	if len(items) != 25 {
		t.Fatalf("max_new_per_poll=0 should disable the cap: got %d of 25", len(items))
	}
}

func TestPollPrunesResolvedProblemsFromSeen(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	problems := []map[string]any{mkProblem("pid-1", "P-1", "Down", started)}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, nil)

	if _, err := a.Poll(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(a.seen) != 1 {
		t.Fatalf("expected 1 tracked problem, got %d", len(a.seen))
	}

	// The problem resolves (disappears from the open set): it must be pruned
	// so the seen map cannot grow without bound.
	problems = []map[string]any{}
	if _, err := a.Poll(context.Background(), time.Time{}); err != nil {
		t.Fatalf("Poll after resolve: %v", err)
	}
	if len(a.seen) != 0 {
		t.Errorf("resolved problem still tracked in seen (%d entries) — memory leak", len(a.seen))
	}
}

func TestTitleFallbackAndKeyOnlyTags(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	problems := []map[string]any{{
		"problemId":     "pid-1",
		"displayId":     "P-77",
		"title":         "",
		"impactLevel":   "SERVICES",
		"severityLevel": "ERROR",
		"status":        "OPEN",
		"startTime":     started.UnixMilli(),
		"endTime":       -1,
		"entityTags": []map[string]any{
			{"context": "CONTEXTLESS", "key": "canary", "stringRepresentation": "canary"},
		},
	}}
	srv := newTestServer(t, &problems, nil)
	a := connect(t, srv.URL, nil)

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Title != "problem P-77" {
		t.Errorf("Title = %q, want displayId fallback", items[0].Title)
	}
	found := false
	for _, l := range items[0].Labels {
		if l == "canary" {
			found = true
		}
	}
	if !found {
		t.Errorf("Labels = %v, want key-only tag rendered as %q", items[0].Labels, "canary")
	}
}

func TestPollPropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	a := connect(t, srv.URL, nil)

	if _, err := a.Poll(context.Background(), time.Time{}); err == nil {
		t.Fatal("expected Poll to surface the API error")
	}
}

func itemStub() model.SourceItem {
	return model.SourceItem{ID: "pid-1", SourceID: "prod-problems"}
}

func resultStub() model.RunResult {
	return model.RunResult{Success: true}
}
