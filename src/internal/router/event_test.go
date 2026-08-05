package router

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

func eventRouterConfig(triggers map[string]*config.TriggerConfig) *config.Config {
	cfg := &config.Config{Version: "1"}
	for id, tr := range triggers {
		cfg.Workflows = append(cfg.Workflows, config.WorkflowConfig{
			ID:      id,
			Trigger: tr,
			Steps:   []config.StepConfig{{ID: "s", Agent: "eng"}},
		})
	}
	return cfg
}

func collabEvent(kind, body string) model.SourceEvent {
	return model.SourceEvent{
		ID: "comment-1", SourceID: "github", Kind: kind, PRNumber: 7,
		Author: "alice", AuthorAssociation: "COLLABORATOR", Body: body,
	}
}

func TestRouteEvent_KindAndCommentContains(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"on-comment": {On: config.TriggerOnPRComment, CommentContains: "@apiary"},
		"on-reject":  {On: config.TriggerOnPRReviewChangesRequest},
	}))
	if err != nil {
		t.Fatal(err)
	}

	if m := r.RouteEvent(collabEvent("pr_comment", "hey @Apiary fix lint"), nil); len(m) != 1 || m[0].Route.ID != "on-comment" {
		t.Errorf("matching comment must route to on-comment, got %v", m)
	}
	if m := r.RouteEvent(collabEvent("pr_comment", "no mention"), nil); len(m) != 0 {
		t.Errorf("comment without the marker must not match, got %v", m)
	}
	if m := r.RouteEvent(collabEvent("pr_review_changes_requested", "please fix"), nil); len(m) != 1 || m[0].Route.ID != "on-reject" {
		t.Errorf("changes_requested must route to on-reject, got %v", m)
	}
}

func TestRouteEvent_DefaultAuthorGateIsCollaboratorsOnly(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"wf": {On: config.TriggerOnPRComment},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ev := collabEvent("pr_comment", "go")
	ev.AuthorAssociation = "NONE"
	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("a stranger (association NONE) must not trigger by default, got %v", m)
	}
	ev.AuthorAssociation = "OWNER"
	if m := r.RouteEvent(ev, nil); len(m) != 1 {
		t.Errorf("an OWNER must trigger by default, got %v", m)
	}
}

func TestRouteEvent_AuthorsListOverridesAssociation(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"wf": {On: config.TriggerOnPRComment, Authors: []string{"Bob"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ev := collabEvent("pr_comment", "go")
	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("author not in authors list must not trigger even as COLLABORATOR, got %v", m)
	}
	ev.Author, ev.AuthorAssociation = "bob", "NONE"
	if m := r.RouteEvent(ev, nil); len(m) != 1 {
		t.Errorf("listed author must trigger regardless of association, got %v", m)
	}
}

func TestRouteEvent_ItemCriteriaRequireRelatedTask(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"wf": {On: config.TriggerOnPRComment, Match: config.RouteMatch{Source: "github", Labels: []string{"apiary"}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ev := collabEvent("pr_comment", "go")

	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("label criteria with no related task must not match, got %v", m)
	}
	task := &model.InternalTask{ID: "T1", Metadata: model.TaskMetadata{Source: "github", Labels: []string{"apiary"}}}
	if m := r.RouteEvent(ev, task); len(m) != 1 {
		t.Errorf("label criteria must match against the related task, got %v", m)
	}
	task.Metadata.Labels = nil
	if m := r.RouteEvent(ev, task); len(m) != 0 {
		t.Errorf("related task without the label must not match, got %v", m)
	}
}

func TestRouteEvent_ItemRoutesAndEventRoutesAreDisjoint(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"items":  {Match: config.RouteMatch{Source: "github"}},
		"events": {On: config.TriggerOnPRComment, Match: config.RouteMatch{Source: "github"}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Item routing never selects an event route.
	task := model.InternalTask{ID: "T1", Metadata: model.TaskMetadata{Source: "github"}}
	for _, m := range r.RouteAll(task) {
		if m.Route.ID == "events" {
			t.Error("RouteAll must not match an event route")
		}
	}
	// Event routing never selects an item route.
	for _, m := range r.RouteEvent(collabEvent("pr_comment", "go"), &task) {
		if m.Route.ID == "items" {
			t.Error("RouteEvent must not match an item route")
		}
	}
	if !r.HasEventRoutes() {
		t.Error("HasEventRoutes must be true when an event trigger exists")
	}
}
