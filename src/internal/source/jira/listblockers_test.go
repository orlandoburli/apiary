package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issueLinksBody is GET /issue/10042?fields=issuelinks for an issue with one
// open blocker (inward "Blocks"), one resolved blocker, one outward "Blocks"
// link (an issue IT blocks — not a blocker), and one unrelated link type.
const issueLinksBody = `{
	"id": "10042", "key": "PSP-42",
	"fields": {
		"issuelinks": [
			{
				"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": {
					"id": "10049", "key": "PSP-49",
					"fields": {"summary": "Register PAS purpose", "status": {"name": "In Progress", "statusCategory": {"key": "indeterminate"}}}
				}
			},
			{
				"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
				"inwardIssue": {
					"id": "10050", "key": "PSP-50",
					"fields": {"summary": "Apigee product", "status": {"name": "Concluído", "statusCategory": {"key": "done"}}}
				}
			},
			{
				"type": {"name": "Blocks", "inward": "is blocked by", "outward": "blocks"},
				"outwardIssue": {
					"id": "10060", "key": "PSP-60",
					"fields": {"summary": "Downstream task", "status": {"name": "To Do", "statusCategory": {"key": "new"}}}
				}
			},
			{
				"type": {"name": "Relates", "inward": "relates to", "outward": "relates to"},
				"inwardIssue": {
					"id": "10070", "key": "PSP-70",
					"fields": {"summary": "Related doc", "status": {"name": "To Do", "statusCategory": {"key": "new"}}}
				}
			}
		]
	}
}`

func blockersServer(t *testing.T, body string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issue/10042") && r.URL.Query().Get("fields") == "issuelinks" {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return newTestAdapter(srv.URL)
}

func TestListBlockers_InwardBlocksLinksOnly(t *testing.T) {
	a := blockersServer(t, issueLinksBody)

	blockers, err := a.ListBlockers(context.Background(), "10042", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 2 {
		t.Fatalf("got %d blockers, want 2 (inward Blocks only): %+v", len(blockers), blockers)
	}

	open, done := blockers[0], blockers[1]
	if open.ID != "10049" || open.Number != "PSP-49" || open.Title != "Register PAS purpose" {
		t.Errorf("open blocker mapped wrong: %+v", open)
	}
	if open.State != "In Progress" {
		t.Errorf("open blocker state = %q, want raw status name", open.State)
	}
	if done.State != "done" {
		t.Errorf("Done-category blocker state = %q, want normalized \"done\"", done.State)
	}
	if open.Merged || done.Merged {
		t.Error("jira blockers must never report Merged (no PR visibility)")
	}
}

func TestListBlockers_CustomLinkType(t *testing.T) {
	a := blockersServer(t, issueLinksBody)

	// "Relates" as the blocking relation: only the inward Relates link matches.
	blockers, err := a.ListBlockers(context.Background(), "10042", "Relates")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].Number != "PSP-70" {
		t.Fatalf("got %+v, want only PSP-70 for link type Relates", blockers)
	}
}

func TestListBlockers_NoLinks(t *testing.T) {
	a := blockersServer(t, `{"id": "10042", "key": "PSP-42", "fields": {"issuelinks": []}}`)

	blockers, err := a.ListBlockers(context.Background(), "10042", "")
	if err != nil {
		t.Fatalf("ListBlockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("got %+v, want none", blockers)
	}
}
