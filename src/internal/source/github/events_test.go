package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// newEventTestAdapter builds an adapter against a fake GitHub API. The handler
// map is keyed by path (query stripped); unmapped paths return an empty JSON
// array so pagination loops terminate.
func newEventTestAdapter(t *testing.T, handlers map[string]any) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if payload, ok := handlers[r.URL.Path]; ok {
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{}
	a.SetID("github")
	if err := a.Connect(context.Background(), map[string]any{"repo": "o/r", "base_url": srv.URL}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPollPREvents_CommentsReviewsAndExclusions(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newer := since.Add(time.Hour).Format(time.RFC3339)
	older := since.Add(-time.Hour).Format(time.RFC3339)

	handlers := map[string]any{
		"/user": map[string]any{"login": "apiary-daemon"},
		"/repos/o/r/issues/comments": []map[string]any{
			// PR conversation comment by a collaborator — the event we want.
			{"id": 1, "body": "@apiary fix lint", "created_at": newer,
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-1",
				"author_association": "COLLABORATOR", "user": map[string]any{"login": "alice", "type": "User"}},
			// Comment on a plain issue — not a PR event.
			{"id": 2, "body": "n/a", "created_at": newer,
				"html_url":           "https://github.com/o/r/issues/9#issuecomment-2",
				"author_association": "OWNER", "user": map[string]any{"login": "alice", "type": "User"}},
			// The daemon's own comment — excluded (loop prevention).
			{"id": 3, "body": "✓ Apiary run complete", "created_at": newer,
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-3",
				"author_association": "CONTRIBUTOR", "user": map[string]any{"login": "Apiary-Daemon", "type": "User"}},
			// A bot comment — excluded.
			{"id": 4, "body": "coverage report", "created_at": newer,
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-4",
				"author_association": "NONE", "user": map[string]any{"login": "codecov[bot]", "type": "Bot"}},
			// An edited old comment: updated now (so returned by since=...) but
			// created before the watermark — must not re-fire.
			{"id": 5, "body": "old edited", "created_at": older,
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-5",
				"author_association": "MEMBER", "user": map[string]any{"login": "bob", "type": "User"}},
		},
		"/repos/o/r/pulls/comments": []map[string]any{
			{"id": 10, "body": "inline nit", "created_at": newer,
				"html_url":           "https://github.com/o/r/pull/7#discussion_r10",
				"pull_request_url":   "https://api.github.com/repos/o/r/pulls/7",
				"author_association": "MEMBER", "user": map[string]any{"login": "bob", "type": "User"}},
		},
		"/repos/o/r/pulls": []map[string]any{
			{"number": 7, "state": "open", "updated_at": newer, "body": "Fixes #42",
				"title": "PSP-348 fix login", "head": map[string]any{"ref": "feat/PSP-348-login"},
				"html_url": "https://github.com/o/r/pull/7", "user": map[string]any{"login": "dave", "type": "User"}},
			{"number": 3, "state": "open", "updated_at": older, "body": "", "html_url": ""},
		},
		"/repos/o/r/pulls/7": map[string]any{
			"number": 7, "state": "open", "body": "Fixes #42",
			"title": "PSP-348 fix login", "head": map[string]any{"ref": "feat/PSP-348-login"},
			"html_url": "https://github.com/o/r/pull/7",
			"user":     map[string]any{"login": "dave", "type": "User"}},
		"/repos/o/r/pulls/7/reviews": []map[string]any{
			{"id": 100, "state": "CHANGES_REQUESTED", "body": "needs tests", "submitted_at": newer,
				"author_association": "OWNER", "user": map[string]any{"login": "carol", "type": "User"}},
			{"id": 101, "state": "COMMENTED", "body": "fyi", "submitted_at": newer,
				"author_association": "OWNER", "user": map[string]any{"login": "carol", "type": "User"}},
			{"id": 102, "state": "APPROVED", "body": "", "submitted_at": older,
				"author_association": "OWNER", "user": map[string]any{"login": "carol", "type": "User"}},
		},
	}

	a := newEventTestAdapter(t, handlers)
	events, err := a.PollPREvents(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]model.SourceEvent{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	if len(events) != 3 {
		ids := make([]string, 0, len(events))
		for _, ev := range events {
			ids = append(ids, ev.ID)
		}
		t.Fatalf("expected 3 events (comment-1, review-comment-10, review-100), got %d: %s", len(events), strings.Join(ids, ", "))
	}

	cm := byID["comment-1"]
	if cm.Kind != model.EventPRComment || cm.PRNumber != 7 || cm.Author != "alice" || cm.AuthorAssociation != "COLLABORATOR" {
		t.Errorf("comment-1 = %+v", cm)
	}
	if cm.RelatedItemID != "42" {
		t.Errorf("comment-1 related item = %q, want 42 (from \"Fixes #42\")", cm.RelatedItemID)
	}
	if cm.PRTitle != "PSP-348 fix login" || cm.PRBranch != "feat/PSP-348-login" || cm.PRBody != "Fixes #42" {
		t.Errorf("comment-1 PR metadata = title %q branch %q body %q", cm.PRTitle, cm.PRBranch, cm.PRBody)
	}
	if want := []string{"PSP-348"}; !reflect.DeepEqual(cm.TicketRefs, want) {
		t.Errorf("comment-1 ticket refs = %v, want %v", cm.TicketRefs, want)
	}
	if cm.PRURL != "https://github.com/o/r/pull/7" {
		t.Errorf("comment-1 pr url = %q", cm.PRURL)
	}

	if rc := byID["review-comment-10"]; rc.Kind != model.EventPRComment || rc.PRNumber != 7 || rc.Author != "bob" {
		t.Errorf("review-comment-10 = %+v", rc)
	}
	if rv := byID["review-100"]; rv.Kind != model.EventPRReviewChangesRequest || rv.Author != "carol" || rv.Body != "needs tests" {
		t.Errorf("review-100 = %+v", rv)
	}
	for id, ev := range byID {
		if ev.PRAuthor != "dave" {
			t.Errorf("%s pr author = %q, want dave", id, ev.PRAuthor)
		}
	}
}

// A failed PR fetch must leave PRAuthor empty rather than drop the event: the
// router decides, and a trigger with pr_authors fails closed on the empty value.
func TestPollPREvents_PRAuthorEmptyWhenPRFetchFails(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a := newEventTestAdapter(t, map[string]any{
		"/user": map[string]any{"login": "apiary-daemon"},
		"/repos/o/r/issues/comments": []map[string]any{
			{"id": 1, "body": "hi", "created_at": since.Add(time.Hour).Format(time.RFC3339),
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-1",
				"author_association": "MEMBER", "user": map[string]any{"login": "alice", "type": "User"}},
		},
		// /repos/o/r/pulls/7 is unmapped: its "[]" body cannot decode into a PR.
	})

	events, err := a.PollPREvents(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PRAuthor != "" {
		t.Fatalf("want one event with an empty PR author, got %+v", events)
	}
}

// A ticket key is picked up from the PR body when the title and branch have
// none, and a bare GitHub issue reference is not mistaken for one.
func TestPollPREvents_TicketRefFromBody(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	a := newEventTestAdapter(t, map[string]any{
		"/user": map[string]any{"login": "apiary-daemon"},
		"/repos/o/r/issues/comments": []map[string]any{
			{"id": 1, "body": "hi", "created_at": since.Add(time.Hour).Format(time.RFC3339),
				"html_url":           "https://github.com/o/r/pull/7#issuecomment-1",
				"author_association": "MEMBER", "user": map[string]any{"login": "alice", "type": "User"}},
		},
		"/repos/o/r/pulls/7": map[string]any{
			"number": 7, "title": "Fix login", "body": "Closes #42, tracks PSP-348",
			"head": map[string]any{"ref": "fix-login"},
			"user": map[string]any{"login": "dave", "type": "User"}},
	})

	events, err := a.PollPREvents(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want one event, got %d", len(events))
	}
	ev := events[0]
	if want := []string{"PSP-348"}; !reflect.DeepEqual(ev.TicketRefs, want) {
		t.Errorf("TicketRefs = %v, want %v", ev.TicketRefs, want)
	}
	if ev.RelatedItemID != "42" {
		t.Errorf("RelatedItemID = %q, want the GitHub issue reference 42", ev.RelatedItemID)
	}
}

func TestFindRelatedIssue(t *testing.T) {
	cases := []struct {
		body string
		pr   int
		want string
	}{
		{"Closes #42", 7, "42"},
		{"fixes o/r#12 and mentions #99", 7, "12"},
		{"Resolved: #8", 7, "8"},
		{"see #7 and #21", 7, "21"}, // self-reference skipped, fallback ref wins
		{"no refs here", 7, ""},
	}
	for _, c := range cases {
		if got := findRelatedIssue(c.body, c.pr); got != c.want {
			t.Errorf("findRelatedIssue(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}
