package prometheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// groupServer serves GET /api/v2/alerts/groups and records silences, so group
// tests can drive polling, resolution, and ack_via_silence against one server.
type groupServer struct {
	*httptest.Server

	mu     sync.Mutex
	groups []map[string]any
	posted []silence
}

func newGroupServer(t *testing.T) *groupServer {
	t.Helper()
	s := &groupServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/alerts/groups":
			s.mu.Lock()
			groups := s.groups
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if groups == nil {
				groups = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(groups)

		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
			var body silence
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.posted = append(s.posted, body)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"silenceID": "sil-1"})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *groupServer) set(groups []map[string]any) {
	s.mu.Lock()
	s.groups = groups
	s.mu.Unlock()
}

func (s *groupServer) silences() []silence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]silence(nil), s.posted...)
}

// mkGroup builds an AlertGroup payload with the given group labels and members.
func mkGroup(groupLabels map[string]string, members ...map[string]any) map[string]any {
	return map[string]any{
		"labels":   groupLabels,
		"receiver": map[string]any{"name": "oncall"},
		"alerts":   members,
	}
}

// mkMember is mkAlert with control over the per-alert labels.
func mkMember(fingerprint string, startsAt time.Time, labels map[string]string) map[string]any {
	al := mkAlert(fingerprint, labels["alertname"], labels["severity"], startsAt)
	al["labels"] = labels
	return al
}

func groupAdapter(t *testing.T, url string, extra map[string]any) *Adapter {
	t.Helper()
	cfg := map[string]any{"dispatch_by": "group", "min_age": "0s"}
	for k, v := range extra {
		cfg[k] = v
	}
	return connect(t, url, cfg)
}

func pollItems(t *testing.T, a *Adapter) []model.SourceItem {
	t.Helper()
	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	return items
}

// One group of three alerts becomes one task, not three.
func TestGroupPollDispatchesOneItemPerGroup(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	srv := newGroupServer(t)
	srv.set([]map[string]any{
		mkGroup(map[string]string{"alertname": "HighErrorRate", "team": "platform"},
			mkMember("aaa1", start, map[string]string{"alertname": "HighErrorRate", "team": "platform", "severity": "critical", "instance": "web-1"}),
			mkMember("bbb2", start.Add(time.Minute), map[string]string{"alertname": "HighErrorRate", "team": "platform", "severity": "critical", "instance": "web-2"}),
			mkMember("ccc3", start.Add(2*time.Minute), map[string]string{"alertname": "HighErrorRate", "team": "platform", "severity": "warning", "instance": "web-3"}),
		),
	})
	a := groupAdapter(t, srv.URL, nil)

	items := pollItems(t, a)
	if len(items) != 1 {
		t.Fatalf("expected 1 group item, got %d", len(items))
	}
	it := items[0]

	if it.Type != "alert_group" {
		t.Errorf("Type = %q, want alert_group", it.Type)
	}
	if !strings.Contains(it.Title, "HighErrorRate") || !strings.Contains(it.Title, "3 alerts") {
		t.Errorf("Title = %q, want the alert name and member count", it.Title)
	}
	// severity differs across members, so it is not a group-wide label.
	for _, l := range it.Labels {
		if strings.HasPrefix(l, "severity:") {
			t.Errorf("severity must not be a group label when members disagree: %v", it.Labels)
		}
	}
	if it.Priority != "critical" {
		t.Errorf("Priority = %q, want the worst member severity (critical)", it.Priority)
	}
	if got := it.Metadata["alertCount"]; got != 3 {
		t.Errorf("metadata alertCount = %v, want 3", got)
	}
	for _, want := range []string{"web-1", "web-2", "web-3"} {
		if !strings.Contains(it.Description, want) {
			t.Errorf("description missing member %q", want)
		}
	}
}

// A label every member shares is true of the group, so trigger matching sees it.
func TestGroupLabelsIncludeSharedMemberLabels(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	srv := newGroupServer(t)
	srv.set([]map[string]any{
		mkGroup(map[string]string{"alertname": "HighErrorRate"},
			mkMember("aaa1", start, map[string]string{"alertname": "HighErrorRate", "severity": "critical", "instance": "web-1"}),
			mkMember("bbb2", start, map[string]string{"alertname": "HighErrorRate", "severity": "critical", "instance": "web-2"}),
		),
	})
	a := groupAdapter(t, srv.URL, nil)

	items := pollItems(t, a)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	var hasSeverity, hasInstance bool
	for _, l := range items[0].Labels {
		if l == "severity:critical" {
			hasSeverity = true
		}
		if strings.HasPrefix(l, "instance:") {
			hasInstance = true
		}
	}
	if !hasSeverity {
		t.Errorf("labels = %v, want severity:critical (shared by every member)", items[0].Labels)
	}
	if hasInstance {
		t.Errorf("labels = %v, must not include instance (members disagree)", items[0].Labels)
	}
}

// The epoch is pinned: churn inside a live group must not change its identity.
func TestGroupIdentityIsStableAcrossMembershipChurn(t *testing.T) {
	start := time.Now().Add(-20 * time.Minute)
	oldest := mkMember("aaa1", start, map[string]string{"alertname": "HighErrorRate", "instance": "web-1"})
	joiner := mkMember("bbb2", time.Now().Add(-5*time.Minute), map[string]string{"alertname": "HighErrorRate", "instance": "web-2"})

	srv := newGroupServer(t)
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, oldest)})
	a := groupAdapter(t, srv.URL, nil)

	first := pollItems(t, a)
	if len(first) != 1 {
		t.Fatalf("expected 1 item, got %d", len(first))
	}
	id := first[0].ID

	// A new alert joins the group.
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, oldest, joiner)})
	if got := pollItems(t, a); len(got) != 1 || got[0].ID != id {
		t.Fatalf("id changed when an alert joined: %v, want %s", got, id)
	}

	// The OLDEST alert resolves — the case that would move a live-min epoch.
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, joiner)})
	got := pollItems(t, a)
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("id changed when the oldest member resolved: %v, want %s — the epoch must stay pinned", got, id)
	}
}

// A group that empties ends its cycle; firing again is a new item.
func TestGroupRefireIsANewItem(t *testing.T) {
	srv := newGroupServer(t)
	first := mkMember("aaa1", time.Now().Add(-20*time.Minute), map[string]string{"alertname": "HighErrorRate"})
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, first)})
	a := groupAdapter(t, srv.URL, nil)

	firstItems := pollItems(t, a)
	if len(firstItems) != 1 {
		t.Fatalf("expected 1 item, got %d", len(firstItems))
	}

	// The group empties: cycle over.
	srv.set(nil)
	if got := pollItems(t, a); len(got) != 0 {
		t.Fatalf("expected no items for an empty group, got %v", got)
	}

	// It fires again with a later start.
	second := mkMember("ddd4", time.Now().Add(-time.Minute), map[string]string{"alertname": "HighErrorRate"})
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, second)})
	got := pollItems(t, a)
	if len(got) != 1 {
		t.Fatalf("expected 1 item after re-fire, got %d", len(got))
	}
	if got[0].ID == firstItems[0].ID {
		t.Fatalf("re-fire reused the previous cycle's id %s — it must dispatch as new work", got[0].ID)
	}
}

// Members that already resolved but linger must not keep a cycle alive.
func TestGroupIgnoresResolvedMembers(t *testing.T) {
	srv := newGroupServer(t)
	dead := mkMember("aaa1", time.Now().Add(-20*time.Minute), map[string]string{"alertname": "HighErrorRate"})
	dead["endsAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, dead)})
	a := groupAdapter(t, srv.URL, nil)

	if got := pollItems(t, a); len(got) != 0 {
		t.Fatalf("a group whose only member resolved must not dispatch, got %v", got)
	}
}

// min_age dampens the group cycle, and maturing must not change the id.
func TestGroupMinAgeDefersWithoutChangingIdentity(t *testing.T) {
	srv := newGroupServer(t)
	young := mkMember("aaa1", time.Now().Add(-time.Second), map[string]string{"alertname": "HighErrorRate"})
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, young)})
	a := connect(t, srv.URL, map[string]any{"dispatch_by": "group", "min_age": "1h"})

	if got := pollItems(t, a); len(got) != 0 {
		t.Fatalf("a too-young group must be deferred, got %v", got)
	}
	// The epoch was pinned while deferred; it must survive to the dispatch.
	a.mu.Lock()
	pinned := len(a.groupEpoch)
	a.mu.Unlock()
	if pinned != 1 {
		t.Fatalf("expected the deferred group's epoch to stay pinned, got %d entries", pinned)
	}
}

// The storm cap counts groups, not member alerts.
func TestGroupStormCapCountsGroups(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	srv := newGroupServer(t)
	var groups []map[string]any
	for _, name := range []string{"A", "B", "C"} {
		groups = append(groups, mkGroup(map[string]string{"alertname": name},
			mkMember("fp"+name, start, map[string]string{"alertname": name})))
	}
	srv.set(groups)
	a := connect(t, srv.URL, map[string]any{"dispatch_by": "group", "min_age": "0s", "max_new_per_poll": 2})

	if got := pollItems(t, a); len(got) != 2 {
		t.Fatalf("expected the storm cap to admit 2 groups, got %d", len(got))
	}
	if got := pollItems(t, a); len(got) != 3 {
		t.Fatalf("expected the deferred group on the next poll (3 total), got %d", len(got))
	}
}

// Resolution must be evaluated against group ids, not per-alert ids — otherwise
// every running investigation would look resolved.
func TestGroupResolvedItemsUsesGroupIdentity(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	srv := newGroupServer(t)
	member := mkMember("aaa1", start, map[string]string{"alertname": "HighErrorRate"})
	srv.set([]map[string]any{mkGroup(map[string]string{"alertname": "HighErrorRate"}, member)})
	a := groupAdapter(t, srv.URL, nil)

	items := pollItems(t, a)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	id := items[0].ID

	// Still firing: two checks, still nothing resolved.
	for i := 0; i < 2; i++ {
		got, err := a.ResolvedItems(context.Background(), []string{id})
		if err != nil {
			t.Fatalf("ResolvedItems: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("live group reported resolved: %v", got)
		}
	}

	// The group empties.
	srv.set(nil)
	if _, err := a.ResolvedItems(context.Background(), []string{id}); err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	got, err := a.ResolvedItems(context.Background(), []string{id})
	if err != nil {
		t.Fatalf("ResolvedItems: %v", err)
	}
	if len(got) != 1 || got[0] != id {
		t.Fatalf("ResolvedItems = %v, want [%s]", got, id)
	}
}

// ack_via_silence on a group silences the whole group, on labels true of every
// member.
func TestGroupAcknowledgeSilencesWholeGroup(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	srv := newGroupServer(t)
	srv.set([]map[string]any{
		mkGroup(map[string]string{"alertname": "HighErrorRate", "team": "platform"},
			mkMember("aaa1", start, map[string]string{"alertname": "HighErrorRate", "team": "platform", "instance": "web-1"}),
			mkMember("bbb2", start, map[string]string{"alertname": "HighErrorRate", "team": "platform", "instance": "web-2"}),
		),
	})
	a := groupAdapter(t, srv.URL, map[string]any{"ack_via_silence": true})

	items := pollItems(t, a)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if err := a.Acknowledge(context.Background(), items[0], model.AckActionInProgress); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	got := srv.silences()
	if len(got) != 1 {
		t.Fatalf("expected 1 silence, got %d", len(got))
	}
	m := matcherMap(t, got[0])
	if m["alertname"] != "HighErrorRate" || m["team"] != "platform" {
		t.Errorf("matchers = %v, want the group labels", m)
	}
	if _, ok := m["instance"]; ok {
		t.Errorf("matchers = %v, must not pin instance — that would silence only one member", m)
	}
}

// Group mode is opt-in; the default stays one task per alert.
func TestDispatchByDefaultsToAlert(t *testing.T) {
	a := connect(t, "http://localhost:9093", nil)
	if a.byGroup {
		t.Fatal("dispatch_by defaulted to group; the per-alert default must be preserved")
	}
	a = connect(t, "http://localhost:9093", map[string]any{"dispatch_by": "alert"})
	if a.byGroup {
		t.Fatal("dispatch_by: alert enabled group mode")
	}
}

func TestConnectRejectsUnknownDispatchBy(t *testing.T) {
	for _, v := range []any{"groups", "per-alert", 7} {
		a := &Adapter{}
		err := a.Connect(context.Background(), map[string]any{
			"alertmanager_url": "http://localhost:9093",
			"dispatch_by":      v,
		})
		if err == nil {
			t.Errorf("dispatch_by=%v: expected an error", v)
		}
	}
}

// Two groups routing to different receivers are distinct even with equal labels.
func TestGroupKeyIncludesReceiver(t *testing.T) {
	g1 := alertGroup{Labels: map[string]string{"alertname": "X"}, Receiver: groupReceiver{Name: "oncall"}}
	g2 := alertGroup{Labels: map[string]string{"alertname": "X"}, Receiver: groupReceiver{Name: "pager"}}
	if groupKey(g1) == groupKey(g2) {
		t.Fatalf("group keys collide across receivers: %s", groupKey(g1))
	}
}

// The key must not depend on Go's map iteration order.
func TestGroupKeyIsDeterministic(t *testing.T) {
	labels := map[string]string{"alertname": "X", "team": "platform", "env": "prod", "region": "eu"}
	want := groupKey(alertGroup{Labels: labels, Receiver: groupReceiver{Name: "oncall"}})
	for i := 0; i < 50; i++ {
		clone := map[string]string{}
		for k, v := range labels {
			clone[k] = v
		}
		if got := groupKey(alertGroup{Labels: clone, Receiver: groupReceiver{Name: "oncall"}}); got != want {
			t.Fatalf("group key varies across iterations: %q vs %q", got, want)
		}
	}
}
