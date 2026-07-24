package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsCollaborator_True(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/collaborators/alice" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := &Adapter{owner: "owner", repo: "repo", client: newClient(srv.URL, "")}
	ok, err := a.IsCollaborator(context.Background(), "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected alice to be a collaborator")
	}
}

func TestIsCollaborator_False(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &Adapter{owner: "owner", repo: "repo", client: newClient(srv.URL, "")}
	ok, err := a.IsCollaborator(context.Background(), "mallory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("mallory should not be a collaborator")
	}
}

func TestIsCollaborator_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	a := &Adapter{owner: "owner", repo: "repo", client: newClient(srv.URL, "")}
	_, err := a.IsCollaborator(context.Background(), "someone")
	if err == nil {
		t.Error("expected error for non-200/204/404 response")
	}
}

// TestToSourceItemPopulatesAuthorLogin verifies that the GitHub adapter sets
// AuthorLogin from the issue's User.Login field.
func TestToSourceItemPopulatesAuthorLogin(t *testing.T) {
	a := &Adapter{id: "gh", owner: "o", repo: "r", client: newClient("", "")}
	item := issue{
		Number: 42,
		Title:  "Test issue",
		State:  "open",
		User:   user{Login: "alice"},
	}
	si := a.toSourceItem(item)
	if si.AuthorLogin != "alice" {
		t.Errorf("expected AuthorLogin=alice, got %q", si.AuthorLogin)
	}
}
