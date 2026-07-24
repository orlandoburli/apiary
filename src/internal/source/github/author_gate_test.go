package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// itemWithAssoc builds a minimal SourceItem whose Metadata contains the given
// author_association value, as toSourceItem would populate it.
func itemWithAssoc(assoc string) model.SourceItem {
	return model.SourceItem{
		ID:       "1",
		Metadata: map[string]any{"author_association": assoc},
	}
}

// TestIsAuthorTrusted_GateDisabled verifies that when require_collaborator is
// false (the default), every author — including NONE and CONTRIBUTOR — is
// treated as trusted and the gate is a no-op.
func TestIsAuthorTrusted_GateDisabled(t *testing.T) {
	a := &Adapter{requireCollaborator: false}
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "NONE", ""} {
		trusted, err := a.IsAuthorTrusted(context.Background(), itemWithAssoc(assoc))
		if err != nil {
			t.Errorf("assoc=%q: unexpected error: %v", assoc, err)
		}
		if !trusted {
			t.Errorf("assoc=%q: want trusted=true when gate disabled, got false", assoc)
		}
	}
}

// TestIsAuthorTrusted_TrustedAssociations verifies that OWNER, MEMBER, and
// COLLABORATOR pass the gate when require_collaborator is true.
func TestIsAuthorTrusted_TrustedAssociations(t *testing.T) {
	a := &Adapter{requireCollaborator: true}
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		trusted, err := a.IsAuthorTrusted(context.Background(), itemWithAssoc(assoc))
		if err != nil {
			t.Errorf("assoc=%q: unexpected error: %v", assoc, err)
		}
		if !trusted {
			t.Errorf("assoc=%q: want trusted=true, got false", assoc)
		}
	}
}

// TestIsAuthorTrusted_UntrustedAssociations verifies that associations other
// than OWNER/MEMBER/COLLABORATOR are blocked by the gate.
func TestIsAuthorTrusted_UntrustedAssociations(t *testing.T) {
	a := &Adapter{requireCollaborator: true}
	for _, assoc := range []string{"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "NONE", "MANNEQUIN"} {
		trusted, err := a.IsAuthorTrusted(context.Background(), itemWithAssoc(assoc))
		if err != nil {
			t.Errorf("assoc=%q: unexpected error: %v", assoc, err)
		}
		if trusted {
			t.Errorf("assoc=%q: want trusted=false, got true", assoc)
		}
	}
}

// TestIsAuthorTrusted_EmptyAssociation verifies fail-open: when author_association
// is absent or empty, the item is treated as trusted so a metadata gap never
// blocks legitimate work.
func TestIsAuthorTrusted_EmptyAssociation(t *testing.T) {
	a := &Adapter{requireCollaborator: true}

	for _, item := range []model.SourceItem{
		{ID: "1", Metadata: map[string]any{"author_association": ""}},
		{ID: "2", Metadata: nil},
		{ID: "3"},
	} {
		trusted, err := a.IsAuthorTrusted(context.Background(), item)
		if err != nil {
			t.Errorf("item %s: unexpected error: %v", item.ID, err)
		}
		if !trusted {
			t.Errorf("item %s: want trusted=true (fail-open on missing data), got false", item.ID)
		}
	}
}

// TestIsAuthorTrusted_CaseInsensitive verifies that association values are
// compared case-insensitively (GitHub documents uppercase but defensive).
func TestIsAuthorTrusted_CaseInsensitive(t *testing.T) {
	a := &Adapter{requireCollaborator: true}
	for _, assoc := range []string{"owner", "Member", "collaborator"} {
		trusted, err := a.IsAuthorTrusted(context.Background(), itemWithAssoc(assoc))
		if err != nil {
			t.Errorf("assoc=%q: unexpected error: %v", assoc, err)
		}
		if !trusted {
			t.Errorf("assoc=%q: want trusted=true (case-insensitive), got false", assoc)
		}
	}
}

// TestParkUntrusted_RemovesTriggerLabelsAndAddsTriage verifies that
// ParkUntrusted removes the configured filter labels and adds "needs-triage".
func TestParkUntrusted_RemovesTriggerLabelsAndAddsTriage(t *testing.T) {
	var deletedLabels []string
	var addedLabels string
	var ensuredLabels []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			idx := strings.LastIndex(r.URL.Path, "/labels/")
			deletedLabels = append(deletedLabels, r.URL.Path[idx+len("/labels/"):])
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels") && !strings.Contains(r.URL.Path, "/repos/o/r/labels"):
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			addedLabels = string(b[:n])
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/labels":
			// ensureLabel
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			ensuredLabels = append(ensuredLabels, string(b[:n]))
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{
		id:           "gh",
		owner:        "o",
		repo:         "r",
		client:       newClient(srv.URL, ""),
		filterLabels: []string{"ai-ready"},
	}
	cell := model.SourceItem{ID: "7", Labels: []string{"ai-ready"}}

	if err := a.ParkUntrusted(context.Background(), cell); err != nil {
		t.Fatalf("ParkUntrusted: %v", err)
	}
	if len(deletedLabels) != 1 || deletedLabels[0] != "ai-ready" {
		t.Errorf("deleted labels = %v, want [ai-ready]", deletedLabels)
	}
	if !strings.Contains(addedLabels, "needs-triage") {
		t.Errorf("added labels body = %q, want needs-triage", addedLabels)
	}
}

// TestParkUntrusted_NoFilterLabels verifies that ParkUntrusted still adds
// "needs-triage" even when the adapter has no configured filter labels.
func TestParkUntrusted_NoFilterLabels(t *testing.T) {
	var ensuredLabels, addedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/labels":
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			ensuredLabels = string(b[:n])
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/5/labels"):
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			addedBody = string(b[:n])
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &Adapter{
		id:     "gh",
		owner:  "o",
		repo:   "r",
		client: newClient(srv.URL, ""),
		// No filterLabels configured.
	}
	if err := a.ParkUntrusted(context.Background(), model.SourceItem{ID: "5"}); err != nil {
		t.Fatalf("ParkUntrusted: %v", err)
	}
	if !strings.Contains(addedBody, "needs-triage") {
		t.Errorf("added labels body = %q, want needs-triage", addedBody)
	}
	_ = ensuredLabels
}

// TestPoll_PopulatesAuthorAssociation verifies that Poll stores the
// author_association from the GitHub response in SourceItem.Metadata so the
// dispatcher can use it for the trust gate without an extra API call.
func TestPoll_PopulatesAuthorAssociation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number": 10, "title": "Collab issue", "state": "open",
			 "author_association": "COLLABORATOR"},
			{"number": 11, "title": "External issue", "state": "open",
			 "author_association": "NONE"}
		]`))
	}))
	defer srv.Close()

	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient(srv.URL, "")}
	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if got, _ := items[0].Metadata["author_association"].(string); got != "COLLABORATOR" {
		t.Errorf("items[0] author_association = %q, want COLLABORATOR", got)
	}
	if got, _ := items[1].Metadata["author_association"].(string); got != "NONE" {
		t.Errorf("items[1] author_association = %q, want NONE", got)
	}
}
