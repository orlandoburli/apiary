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

func TestRouteEvent_KindAndCommentMatches(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"on-comment": {On: config.TriggerOnPRComment, CommentMatches: `(?i)@apiary\s+(fix|update)`},
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
	// The regex is a full Go regexp: mention without a captured verb must miss.
	if m := r.RouteEvent(collabEvent("pr_comment", "@apiary hello"), nil); len(m) != 0 {
		t.Errorf("comment not matching the pattern must not match, got %v", m)
	}
	if m := r.RouteEvent(collabEvent("pr_review_changes_requested", "please fix"), nil); len(m) != 1 || m[0].Route.ID != "on-reject" {
		t.Errorf("changes_requested must route to on-reject, got %v", m)
	}
}

func TestRouterNew_RejectsInvalidCommentMatches(t *testing.T) {
	_, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"bad": {On: config.TriggerOnPRComment, CommentMatches: "@apiary["},
	}))
	if err == nil {
		t.Fatal("router.New must reject an invalid comment_matches pattern")
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

func TestRouteEvent_PRAuthorsFilter(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"comment":  {On: config.TriggerOnPRComment, PRAuthors: []string{"Carol"}},
		"approved": {On: config.TriggerOnPRReviewApproved, PRAuthors: []string{"Carol"}},
		"changes":  {On: config.TriggerOnPRReviewChangesRequest, PRAuthors: []string{"Carol"}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"pr_comment", "pr_review_approved", "pr_review_changes_requested"} {
		ev := collabEvent(kind, "x")
		ev.PRAuthor = "carol" // case differs from the trigger's list
		if m := r.RouteEvent(ev, nil); len(m) != 1 {
			t.Errorf("%s on a listed author's PR must match, got %v", kind, m)
		}
		ev.PRAuthor = "dave"
		if m := r.RouteEvent(ev, nil); len(m) != 0 {
			t.Errorf("%s on another author's PR must not match, got %v", kind, m)
		}
		ev.PRAuthor = ""
		if m := r.RouteEvent(ev, nil); len(m) != 0 {
			t.Errorf("%s with an unknown PR author must fail closed, got %v", kind, m)
		}
	}
}

func TestRouteEvent_PRAuthorsCombinesWithActorGate(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"listed":  {On: config.TriggerOnPRComment, PRAuthors: []string{"carol"}, Authors: []string{"alice"}},
		"default": {On: config.TriggerOnPRComment, PRAuthors: []string{"carol"}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	ev := collabEvent("pr_comment", "x")
	ev.PRAuthor = "carol"
	if m := r.RouteEvent(ev, nil); len(m) != 2 {
		t.Errorf("both gates pass: want 2 matches, got %v", m)
	}

	// Right PR, wrong commenter: authors still gates, and the association
	// default still applies when authors is absent.
	ev.Author, ev.AuthorAssociation = "stranger", "NONE"
	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("pr_authors must not bypass the actor gate, got %v", m)
	}

	// Right commenter, wrong PR: an authors match must not bypass pr_authors.
	ev = collabEvent("pr_comment", "x")
	ev.PRAuthor = "dave"
	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("authors must not bypass pr_authors, got %v", m)
	}
}

func TestEvaluateEvent_PRAuthorsReason(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"mine": {On: config.TriggerOnPRComment, PRAuthors: []string{"carol"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ev := collabEvent("pr_comment", "x")
	ev.PRAuthor = "dave"
	ok, reason := r.evaluateEvent(r.routes[0], ev, nil)
	if want := `pr author "dave" not in trigger pr_authors [carol]`; ok || reason != want {
		t.Errorf("evaluateEvent = %v, %q; want false, %q", ok, reason, want)
	}
}

func TestRouteEvent_IgnoreAuthors(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"default-gate": {On: config.TriggerOnPRComment, IgnoreAuthors: []string{"CI-Account"}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	// The account reports as an ordinary member, so only its name excludes it.
	ev := collabEvent("pr_comment", "build passed")
	ev.Author, ev.AuthorAssociation = "ci-account", "MEMBER"
	if m := r.RouteEvent(ev, nil); len(m) != 0 {
		t.Errorf("ignored author must not match (case-insensitive), got %v", m)
	}
	if m := r.RouteEvent(collabEvent("pr_comment", "x"), nil); len(m) != 1 {
		t.Errorf("an author not on the list must still pass the default gate, got %v", m)
	}
}

func TestRouteEvent_IgnoreAuthorsWinsOverAuthors(t *testing.T) {
	r, err := New(eventRouterConfig(map[string]*config.TriggerConfig{
		"both": {On: config.TriggerOnPRComment, Authors: []string{"alice"}, IgnoreAuthors: []string{"alice"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if m := r.RouteEvent(collabEvent("pr_comment", "x"), nil); len(m) != 0 {
		t.Errorf("ignore_authors must win over authors, got %v", m)
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
