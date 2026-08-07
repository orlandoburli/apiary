package router_test

import (
	"strings"
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

// RouteAllWithSuppressed reports the routes an exclusive winner stopped the
// router from considering — and only the ones that would themselves have run.
// This is what lets the dispatcher explain a task whose exclusive match is later
// removed by a pre-dispatch guard: without it, "no workflow will run" hides the
// fact that a lower-priority workflow matched and was suppressed.
func TestRouteAllWithSuppressed_NamesRoutesBelowTheExclusiveWinner(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		exclusiveRoute("classify", 10, "investigator", config.RouteMatch{Source: "src-a"}),
		agentRoute("review", 20, "reviewer", config.RouteMatch{Source: "src-a"}),
		// Would not have matched anyway: it must not be reported as suppressed.
		agentRoute("labelled", 30, "b", config.RouteMatch{Source: "src-a", Labels: []string{"ai-ready"}}),
		agentRoute("docs", 40, "scribe", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches, suppressed := r.RouteAllWithSuppressed(task())
	if got := ids(matches); len(got) != 1 || got[0] != "classify" {
		t.Fatalf("expected only the exclusive winner to be dispatchable, got %v", got)
	}
	if got := ids(suppressed); len(got) != 2 || got[0] != "review" || got[1] != "docs" {
		t.Fatalf("expected [review docs] suppressed (in priority order, excluding non-matching), got %v", got)
	}

	// The suppressed set is diagnostic only: RouteAll must keep returning exactly
	// the dispatchable matches, or the exclusive claim would fan out after all.
	if got := ids(r.RouteAll(task())); len(got) != 1 || got[0] != "classify" {
		t.Fatalf("RouteAll must not dispatch suppressed routes, got %v", got)
	}
}

// Without an exclusive match nothing is suppressed, whether or not routes match.
func TestRouteAllWithSuppressed_NoExclusiveNoSuppression(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		agentRoute("review", 10, "reviewer", config.RouteMatch{Source: "src-a"}),
		agentRoute("docs", 20, "scribe", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	matches, suppressed := r.RouteAllWithSuppressed(task())
	if len(matches) != 2 {
		t.Fatalf("expected both routes to fan out, got %v", ids(matches))
	}
	if suppressed != nil {
		t.Fatalf("expected no suppression without an exclusive route, got %v", ids(suppressed))
	}
}

// ExplainTask keeps reporting the routes below an exclusive winner instead of
// stopping at it, so the per-route execution events (route.rejected) exist for
// them and name the route that claimed the task.
func TestExplainTask_ReportsSuppressedRoutesBelowExclusive(t *testing.T) {
	r, err := router.New(cfg([]config.RouteConfig{
		exclusiveRoute("classify", 10, "investigator", config.RouteMatch{Source: "src-a"}),
		agentRoute("review", 20, "reviewer", config.RouteMatch{Source: "src-a"}),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	traces := r.ExplainTask(task())
	if len(traces) != 2 {
		t.Fatalf("expected a trace for every route, got %d: %+v", len(traces), traces)
	}
	if !traces[0].Selected || traces[0].RouteID != "classify" {
		t.Fatalf("expected classify to win, got %+v", traces[0])
	}
	below := traces[1]
	if below.RouteID != "review" || below.Selected {
		t.Fatalf("a route below an exclusive winner must be traced but not selected, got %+v", below)
	}
	if !strings.Contains(below.Reason, "classify") || !strings.Contains(below.Reason, "suppressed") {
		t.Fatalf("the reason must name the exclusive route that suppressed it, got %q", below.Reason)
	}
}
