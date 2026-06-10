package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

func TestBuildJQL(t *testing.T) {
	loc := time.FixedZone("UTC-3", -3*60*60)
	since := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		project, userQL string
		since           time.Time
		want            string
	}{
		{
			name: "empty", want: "ORDER BY updated ASC",
		},
		{
			name: "project only", project: "ERP",
			want: `project = "ERP" ORDER BY updated ASC`,
		},
		{
			name: "project and user jql parenthesized", project: "ERP", userQL: "labels = apiary",
			want: `project = "ERP" AND (labels = apiary) ORDER BY updated ASC`,
		},
		{
			// 12:00 UTC = 09:00 UTC-3, minus the 2-minute slack = 08:58.
			name: "since converted to user tz with slack", project: "ERP", since: since,
			want: `project = "ERP" AND updated >= "2026/06/10 08:58" ORDER BY updated ASC`,
		},
		{
			name: "user jql only", userQL: "statusCategory != Done",
			want: `(statusCategory != Done) ORDER BY updated ASC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildJQL(tt.project, tt.userQL, tt.since, loc); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

const issuePageOne = `{
	"issues": [{
		"id": "10001", "key": "ERP-1",
		"fields": {
			"summary": "Add login",
			"description": {"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"the spec"}]}]},
			"status": {"name": "To Do", "statusCategory": {"key": "new"}},
			"priority": {"name": "High"},
			"issuetype": {"name": "Story"},
			"labels": ["Apiary", "backend"],
			"created": "2026-06-01T10:00:00.000-0300",
			"updated": "2026-06-09T15:30:00.000-0300"
		}
	}],
	"nextPageToken": "tok-2"
}`

const issuePageTwo = `{
	"issues": [{
		"id": "10002", "key": "ERP-2",
		"fields": {
			"summary": "Fix logout",
			"description": null,
			"status": {"name": "In Progress", "statusCategory": {"key": "indeterminate"}},
			"priority": null,
			"issuetype": {"name": "Bug"},
			"labels": [],
			"created": "2026-06-02T10:00:00.000-0300",
			"updated": "2026-06-09T16:00:00.000-0300"
		}
	}]
}`

func newTestAdapter(srvURL string) *Adapter {
	return &Adapter{id: "jira-test", baseURL: srvURL, client: newClient(srvURL, "bot@example.com", "tok")}
}

func TestPoll_PaginatesAndMaps(t *testing.T) {
	var jqls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"timeZone": "UTC"}`))
		case "/rest/api/3/search/jql":
			jqls = append(jqls, r.URL.Query().Get("jql"))
			if r.URL.Query().Get("nextPageToken") == "tok-2" {
				_, _ = w.Write([]byte(issuePageTwo))
			} else {
				_, _ = w.Write([]byte(issuePageOne))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	a.project = "ERP"

	cells, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(cells) != 2 {
		t.Fatalf("expected 2 cells across pages, got %d", len(cells))
	}
	if len(jqls) != 2 || !strings.Contains(jqls[0], `project = "ERP"`) {
		t.Errorf("expected 2 search calls with project clause, got %v", jqls)
	}

	c := cells[0]
	if c.ID != "10001" || c.Number != "ERP-1" || c.Title != "Add login" {
		t.Errorf("identity mapping wrong: %+v", c)
	}
	if c.Description != "the spec" {
		t.Errorf("ADF description not flattened: %q", c.Description)
	}
	if c.State != "To Do" || c.Priority != "high" || c.Type != "story" {
		t.Errorf("state/priority/type mapping wrong: %+v", c)
	}
	if len(c.Labels) != 2 || c.Labels[0] != "apiary" {
		t.Errorf("labels not lowercased: %v", c.Labels)
	}
	if c.URL != srv.URL+"/browse/ERP-1" {
		t.Errorf("URL wrong: %s", c.URL)
	}
	// 15:30 -0300 = 18:30 UTC; the colonless offset must parse.
	if !c.UpdatedAt.Equal(time.Date(2026, 6, 9, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("UpdatedAt wrong (jira timestamp layout not parsed?): %v", c.UpdatedAt)
	}

	if cells[1].Priority != "" || cells[1].Description != "" {
		t.Errorf("nil priority/description must map to empty: %+v", cells[1])
	}
}

func TestPoll_ClientSideFiltersAndSinceCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"timeZone": "UTC"}`))
		case "/rest/api/3/search/jql":
			page := strings.Replace(issuePageOne, `"nextPageToken": "tok-2"`, `"nextPageToken": ""`, 1)
			_, _ = w.Write([]byte(page))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	a.SetFilters([]string{"In Progress"}, nil)
	cells, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(cells) != 0 {
		t.Errorf("state filter should drop the To Do issue, got %d cells", len(cells))
	}

	a2 := newTestAdapter(srv.URL)
	a2.SetFilters(nil, []string{"apiary"})
	since := time.Date(2026, 6, 9, 19, 0, 0, 0, time.UTC) // after the issue's 18:30 UTC update
	cells, err = a2.Poll(context.Background(), since)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(cells) != 0 {
		t.Errorf("since cut should drop the stale issue, got %d cells", len(cells))
	}
}

const transitionsBody = `{"transitions": [
	{"id": "11", "name": "Start work", "to": {"name": "In Progress", "statusCategory": {"key": "indeterminate"}}},
	{"id": "21", "name": "Finish", "to": {"name": "Done", "statusCategory": {"key": "done"}}}
]}`

// transitionServer serves the transitions list and captures the POSTed
// transition id; the issue endpoint reports the given current status.
func transitionServer(t *testing.T, currentStatus, categoryKey string, posted *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(transitionsBody))
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req transitionRequest
			_ = json.Unmarshal(body, &req)
			*posted = req.Transition.ID
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/issue/10001"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "10001", "key": "ERP-1",
				"fields": map[string]any{
					"status": map[string]any{"name": currentStatus, "statusCategory": map[string]any{"key": categoryKey}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSetState_MatchesTargetCaseInsensitively(t *testing.T) {
	var posted string
	srv := transitionServer(t, "To Do", "new", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	if err := a.SetState(context.Background(), model.SourceItem{ID: "10001"}, "done"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if posted != "21" {
		t.Errorf("expected transition 21 posted, got %q", posted)
	}
}

func TestSetState_FallsBackToTransitionName(t *testing.T) {
	var posted string
	srv := transitionServer(t, "To Do", "new", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	if err := a.SetState(context.Background(), model.SourceItem{ID: "10001"}, "start work"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if posted != "11" {
		t.Errorf("expected transition 11 posted, got %q", posted)
	}
}

func TestSetState_AlreadyInTargetIsNoop(t *testing.T) {
	var posted string
	srv := transitionServer(t, "Blocked", "indeterminate", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	if err := a.SetState(context.Background(), model.SourceItem{ID: "10001"}, "blocked"); err != nil {
		t.Fatalf("expected no-op success when already in target state: %v", err)
	}
	if posted != "" {
		t.Errorf("no transition should be posted, got %q", posted)
	}
}

func TestSetState_UnreachableListsAvailable(t *testing.T) {
	var posted string
	srv := transitionServer(t, "To Do", "new", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	err := a.SetState(context.Background(), model.SourceItem{ID: "10001"}, "Archived")
	if err == nil {
		t.Fatal("expected error for unreachable state")
	}
	if !strings.Contains(err.Error(), "In Progress, Done") {
		t.Errorf("error should list available targets: %v", err)
	}
}

func TestAcknowledge_UsesConfiguredStartedState(t *testing.T) {
	var posted string
	srv := transitionServer(t, "To Do", "new", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	a.startedState = "In Progress"
	if err := a.Acknowledge(context.Background(), model.SourceItem{ID: "10001"}, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if posted != "11" {
		t.Errorf("expected transition 11 posted, got %q", posted)
	}
}

func TestAcknowledge_FallsBackToIndeterminateCategory(t *testing.T) {
	var posted string
	srv := transitionServer(t, "To Do", "new", &posted)
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	if err := a.Acknowledge(context.Background(), model.SourceItem{ID: "10001"}, model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if posted != "11" {
		t.Errorf("expected the indeterminate-category transition 11, got %q", posted)
	}
}

func TestAcknowledge_OtherActionsAreNoops(t *testing.T) {
	a := newTestAdapter("http://unused.invalid")
	if err := a.Acknowledge(context.Background(), model.SourceItem{ID: "10001"}, model.AckActionSkip); err != nil {
		t.Errorf("skip action must be a no-op: %v", err)
	}
}

func TestAddAndRemoveLabels_AtomicVerbsAndSanitization(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/issue/10001") {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	cell := model.SourceItem{ID: "10001"}

	if err := a.AddLabels(context.Background(), cell, []string{"agent:engineer", "needs review"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if err := a.RemoveLabels(context.Background(), cell, []string{"in-progress"}); err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("expected 2 PUTs, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `{"add":"agent:engineer"}`) || !strings.Contains(bodies[0], `{"add":"needs-review"}`) {
		t.Errorf("add body wrong (space not sanitized?): %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `{"remove":"in-progress"}`) {
		t.Errorf("remove body wrong: %s", bodies[1])
	}
}

func TestConnect_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"missing base_url", map[string]any{"email": "e@x.com", "api_token": "t"}, "base_url"},
		{"relative base_url", map[string]any{"base_url": "yoursite.atlassian.net", "email": "e@x.com", "api_token": "t"}, "http(s)"},
		{"missing email", map[string]any{"base_url": "https://x.atlassian.net", "api_token": "t"}, "email"},
		{"missing api_token", map[string]any{"base_url": "https://x.atlassian.net", "email": "e@x.com"}, "api_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Adapter{}).Connect(context.Background(), tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected error mentioning %q, got %v", tt.want, err)
			}
		})
	}

	ok := map[string]any{
		"base_url": "https://x.atlassian.net/", "email": "e@x.com", "api_token": "t",
		"project": "ERP", "started_state": "In Progress",
	}
	a := &Adapter{}
	if err := a.Connect(context.Background(), ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if a.baseURL != "https://x.atlassian.net" {
		t.Errorf("trailing slash not trimmed: %q", a.baseURL)
	}
	if a.project != "ERP" || a.startedState != "In Progress" {
		t.Errorf("optional fields not stored: %+v", a)
	}
}

func TestClient_RetriesOn429(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "e@x.com", "t")
	if _, err := c.get(context.Background(), "/rest/api/3/myself", nil); err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (429 then 200), got %d", calls)
	}
}

func TestSearchAll_StopsOnRepeatedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always hand back the same token: the guard must terminate the loop.
		_, _ = w.Write([]byte(`{"issues": [], "nextPageToken": "same"}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "e@x.com", "t")
	if _, err := c.searchAll(context.Background(), "project = X"); err != nil {
		t.Fatalf("repeated token must terminate cleanly: %v", err)
	}
}
