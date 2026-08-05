package router

import (
	"fmt"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// HasEventRoutes reports whether any workflow trigger declares an event axis
// (on: pr_*). The dispatcher uses it to skip PR event polling entirely when no
// workflow could ever consume an event.
func (r *Router) HasEventRoutes() bool {
	for _, route := range r.routes {
		if route.IsEventRoute() {
			return true
		}
	}
	return false
}

// RouteEvent evaluates every event trigger against a PR event in priority order
// and returns all matches — mirroring RouteAll's fan-out and Exclusive
// semantics, but over the event axis. task is the InternalTask bound to the
// event's originating work item, or nil when the event has no related item; a
// route whose Match declares item criteria (labels, types, states, priority,
// title_regex) can only match when a related task exists to evaluate them
// against.
func (r *Router) RouteEvent(event model.SourceEvent, task *model.InternalTask) []Match {
	var matches []Match
	for _, route := range r.routes {
		if !route.IsEventRoute() {
			continue
		}
		ok, _ := r.evaluateEvent(route, event, task)
		if !ok {
			continue
		}
		m, ok := r.resolveMatch(route)
		if !ok {
			continue
		}
		matches = append(matches, m)
		if route.Exclusive {
			break
		}
	}
	return matches
}

// evaluateEvent reports whether an event route matches the event (and its
// related task, when bound) plus a human-readable reason for the decision.
func (r *Router) evaluateEvent(route config.RouteConfig, event model.SourceEvent, task *model.InternalTask) (bool, string) {
	if route.On != event.Kind {
		return false, fmt.Sprintf("event kind %q != trigger on %q", event.Kind, route.On)
	}
	if route.Match.Source != "" && event.SourceID != route.Match.Source {
		return false, fmt.Sprintf("source %q != required %q", event.SourceID, route.Match.Source)
	}
	if route.CommentContains != "" &&
		!strings.Contains(strings.ToLower(event.Body), strings.ToLower(route.CommentContains)) {
		return false, fmt.Sprintf("comment does not contain %q", route.CommentContains)
	}
	if ok, reason := authorAllowed(route, event); !ok {
		return false, reason
	}

	// Item-shaped criteria apply to the related task. A route that declares them
	// but has no related task to evaluate them against must not match — matching
	// would silently ignore a declared condition.
	if hasItemCriteria(route.Match) {
		if task == nil {
			return false, "trigger declares item match criteria but the event has no related work item"
		}
		// Source was already checked against the event; clear it so evaluateTarget
		// does not re-require it on the task (whose Metadata.Source equals the
		// event's source anyway for bound items).
		m := route.Match
		m.Source = ""
		scoped := route
		scoped.Match = m
		if ok, reason := r.evaluateTarget(scoped, targetFromTask(*task)); !ok {
			return false, reason
		}
	}

	return true, fmt.Sprintf("matched event %s by %s", event.Kind, event.Author)
}

// authorAllowed applies the trigger's actor gate: an explicit authors list wins;
// otherwise the author's repository association must be in authors_association,
// defaulting to collaborators-only (config.DefaultEventAuthorAssociations).
func authorAllowed(route config.RouteConfig, event model.SourceEvent) (bool, string) {
	if len(route.Authors) > 0 {
		for _, a := range route.Authors {
			if strings.EqualFold(a, event.Author) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("author %q not in trigger authors %v", event.Author, route.Authors)
	}
	allowed := route.AuthorsAssociation
	if len(allowed) == 0 {
		allowed = config.DefaultEventAuthorAssociations
	}
	for _, a := range allowed {
		if strings.EqualFold(a, event.AuthorAssociation) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("author %q association %q not in %v", event.Author, event.AuthorAssociation, allowed)
}

// hasItemCriteria reports whether a match declares any criterion that must be
// evaluated against a work item (everything except source, which is an event
// attribute too).
func hasItemCriteria(m config.RouteMatch) bool {
	return len(m.Labels) > 0 || len(m.ExcludeLabels) > 0 || m.ExcludeLabelPrefix != "" ||
		len(m.Types) > 0 || len(m.Priority) > 0 || len(m.States) > 0 || m.TitleRegex != ""
}
