package router_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

// task builds an InternalTask whose routing attributes mirror a SourceItem, so
// the cell(...) option builders can be reused to describe what RouteAll sees.
func task(opts ...func(*model.SourceItem)) model.InternalTask {
	c := cell(opts...)
	return model.InternalTask{
		Title: c.Title,
		Metadata: model.TaskMetadata{
			Labels:   c.Labels,
			Priority: c.Priority,
			Type:     c.Type,
			Source:   c.SourceID,
			State:    c.State,
		},
	}
}

func ids(matches []router.Match) []string {
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.Route.ID
	}
	return out
}

// exclusiveRoute is agentRoute with the exclusive flag set.
func exclusiveRoute(id string, prio int, agent string, m config.RouteMatch) config.RouteConfig {
	r := agentRoute(id, prio, agent, m)
	r.Exclusive = true
	return r
}

func TestRouteAll_TwoNonExclusiveBothMatch(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("review", 10, "reviewer", config.RouteMatch{Source: "src-a", Labels: []string{"ai-ready"}}),
		agentRoute("docs", 20, "scribe", config.RouteMatch{Source: "src-a", Labels: []string{"ai-ready"}}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches := r.RouteAll(task(withLabels("ai-ready")))
	if got := ids(matches); len(got) != 2 || got[0] != "review" || got[1] != "docs" {
		t.Fatalf("expected fan-out to [review docs], got %v", got)
	}
}

func TestRouteAll_ExclusiveStopsEvaluation(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		exclusiveRoute("classify", 10, "investigator", config.RouteMatch{Source: "src-a"}),
		agentRoute("review", 20, "reviewer", config.RouteMatch{Source: "src-a"}),
		agentRoute("docs", 30, "scribe", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches := r.RouteAll(task())
	if got := ids(matches); len(got) != 1 || got[0] != "classify" {
		t.Fatalf("exclusive trigger should claim the task alone, got %v", got)
	}
}

func TestRouteAll_PriorityOrderingRespected(t *testing.T) {
	// Declared out of priority order; New sorts by priority ascending, and
	// RouteAll must return matches in that order.
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("third", 30, "c", config.RouteMatch{Source: "src-a"}),
		agentRoute("first", 10, "a", config.RouteMatch{Source: "src-a"}),
		agentRoute("second", 20, "b", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches := r.RouteAll(task())
	want := []string{"first", "second", "third"}
	got := ids(matches)
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("priority order wrong: expected %v, got %v", want, got)
		}
	}
}

// A non-exclusive trigger before an exclusive one still fans out, then the
// exclusive one caps evaluation — matches before it are kept.
func TestRouteAll_NonExclusiveThenExclusive(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("review", 10, "reviewer", config.RouteMatch{Source: "src-a"}),
		exclusiveRoute("owner", 20, "owner", config.RouteMatch{Source: "src-a"}),
		agentRoute("never", 30, "ghost", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches := r.RouteAll(task())
	if got := ids(matches); len(got) != 2 || got[0] != "review" || got[1] != "owner" {
		t.Fatalf("expected [review owner] (stop after exclusive owner), got %v", got)
	}
}

func TestRouteAll_NoMatchReturnsNil(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("review", 10, "reviewer", config.RouteMatch{Source: "other-src"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	if matches := r.RouteAll(task()); matches != nil {
		t.Fatalf("expected nil for no match, got %v", ids(matches))
	}
}

// RouteAll honours match conditions beyond source: a task missing a required
// label is excluded from the fan-out while others still match.
func TestRouteAll_PartialMatchFiltersByConditions(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("always", 10, "a", config.RouteMatch{Source: "src-a"}),
		agentRoute("labelled", 20, "b", config.RouteMatch{Source: "src-a", Labels: []string{"ai-ready"}}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	if got := ids(r.RouteAll(task())); len(got) != 1 || got[0] != "always" {
		t.Fatalf("expected only [always] without the label, got %v", got)
	}
	if got := ids(r.RouteAll(task(withLabels("ai-ready")))); len(got) != 2 {
		t.Fatalf("expected both routes with the label, got %v", got)
	}
}
