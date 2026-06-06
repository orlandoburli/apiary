// Package router evaluates routing rules against a Cell and returns the
// first matching worker configuration.
package router

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// Match is the result of a successful route evaluation.
type Match struct {
	Worker config.WorkerConfig
	Route  config.RouteConfig
}

// RouteTrace records why a single route did or didn't match a cell — the
// "decision tree" behind an agent assignment. Returned by Explain.
type RouteTrace struct {
	RouteID  string
	Priority int
	Agent    string
	Worker   string
	Matched  bool
	Reason   string // why it matched, or which condition rejected it
	Selected bool   // true for the route that actually won (first match)
}

// Router evaluates priority-ordered routing rules against incoming Cells.
type Router struct {
	routes  []config.RouteConfig // sorted by priority ascending
	workers map[string]config.WorkerConfig
	regexes map[string]*regexp.Regexp // route ID → compiled regex
}

// New builds a Router from the given config.
// Returns an error if any route's title_regex fails to compile.
func New(cfg *config.Config) (*Router, error) {
	workers := make(map[string]config.WorkerConfig, len(cfg.Workers))
	for _, w := range cfg.Workers {
		workers[w.ID] = w
	}

	// Each workflow's trigger acts as a route whose id equals the workflow id, so
	// the dispatcher (resolveWorkflow) can upgrade the match to the full multi-step
	// definition. The agent is the workflow's first agent step — representative for
	// semaphore admission and logging.
	routes := make([]config.RouteConfig, 0, len(cfg.Workflows))
	for _, wf := range cfg.Workflows {
		if wf.Trigger == nil {
			continue
		}
		routes = append(routes, config.RouteConfig{
			ID:       wf.ID,
			Priority: wf.Trigger.Priority,
			Match:    wf.Trigger.Match,
			Agent:    firstAgentStep(wf),
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Priority < routes[j].Priority
	})

	regexes := make(map[string]*regexp.Regexp)
	for _, r := range routes {
		if r.Match.TitleRegex != "" {
			re, err := regexp.Compile(r.Match.TitleRegex)
			if err != nil {
				return nil, &ErrBadRegex{RouteID: r.ID, Pattern: r.Match.TitleRegex, Err: err}
			}
			regexes[r.ID] = re
		}
	}

	return &Router{routes: routes, workers: workers, regexes: regexes}, nil
}

// firstAgentStep returns a representative agent id for a workflow's trigger
// route — the first agent the workflow will run. It falls back to the workflow
// id so the synthetic route always has a non-empty agent (Router.Route skips a
// matched route that has neither an agent nor a resolvable worker).
func firstAgentStep(wf config.WorkflowConfig) string {
	for _, s := range wf.Steps {
		switch s.StepType() {
		case config.StepTypeAgent:
			if s.Agent != "" {
				return s.Agent
			}
		case config.StepTypeForeach:
			if s.Step != nil && s.Step.Agent != "" {
				return s.Step.Agent
			}
		}
	}
	return wf.ID
}

// Route evaluates all rules against the SourceItem and returns the first match.
// Returns (zero, false) if no rule matches.
func (r *Router) Route(item model.SourceItem) (Match, bool) {
	for _, route := range r.routes {
		if r.matches(route, item) {
			// Agent-based routing: route.Agent is required; worker is for backward compat
			if route.Agent != "" {
				return Match{Route: route}, true
			}
			// Backward compat: fall back to worker if agent not specified
			worker, ok := r.workers[route.Worker]
			if !ok {
				continue
			}
			return Match{Worker: worker, Route: route}, true
		}
	}
	return Match{}, false
}

func (r *Router) matches(route config.RouteConfig, cell model.SourceItem) bool {
	ok, _ := r.evaluate(route, cell)
	return ok
}

// evaluate reports whether a route matches a cell and a human-readable reason.
// On a miss the reason names the first condition that rejected the cell; on a
// hit it summarises which conditions were satisfied.
func (r *Router) evaluate(route config.RouteConfig, cell model.SourceItem) (bool, string) {
	m := route.Match

	if m.Source != "" && cell.SourceID != m.Source {
		return false, fmt.Sprintf("source %q != required %q", cell.SourceID, m.Source)
	}

	if len(m.States) > 0 && !containsInsensitive(m.States, cell.State) {
		return false, fmt.Sprintf("state %q not in %v", cell.State, m.States)
	}

	if len(m.Labels) > 0 {
		cellLabels := toLowerSet(cell.Labels)
		for _, required := range m.Labels {
			if !cellLabels[strings.ToLower(required)] {
				return false, fmt.Sprintf("missing required label %q (cell has %v)", required, cell.Labels)
			}
		}
	}

	if len(m.ExcludeLabels) > 0 {
		cellLabels := toLowerSet(cell.Labels)
		for _, excluded := range m.ExcludeLabels {
			if cellLabels[strings.ToLower(excluded)] {
				return false, fmt.Sprintf("has excluded label %q", excluded)
			}
		}
	}

	if m.ExcludeLabelPrefix != "" {
		prefix := strings.ToLower(m.ExcludeLabelPrefix)
		for _, l := range cell.Labels {
			if strings.HasPrefix(strings.ToLower(l), prefix) {
				return false, fmt.Sprintf("has label %q matching excluded prefix %q", l, m.ExcludeLabelPrefix)
			}
		}
	}

	if len(m.Types) > 0 && !containsInsensitive(m.Types, cell.Type) {
		return false, fmt.Sprintf("type %q not in %v", cell.Type, m.Types)
	}

	if len(m.Priority) > 0 && !containsInsensitive(m.Priority, cell.Priority) {
		return false, fmt.Sprintf("priority %q not in %v", cell.Priority, m.Priority)
	}

	if re, ok := r.regexes[route.ID]; ok {
		if !re.MatchString(cell.Title) {
			return false, fmt.Sprintf("title %q does not match /%s/", cell.Title, m.TitleRegex)
		}
	}

	return true, describeCriteria(m)
}

// describeCriteria summarises the conditions a route requires, for the matched case.
func describeCriteria(m config.RouteMatch) string {
	var parts []string
	if m.Source != "" {
		parts = append(parts, "source="+m.Source)
	}
	if len(m.States) > 0 {
		parts = append(parts, "states="+strings.Join(m.States, ","))
	}
	if len(m.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(m.Labels, ","))
	}
	if len(m.ExcludeLabels) > 0 {
		parts = append(parts, "exclude_labels="+strings.Join(m.ExcludeLabels, ","))
	}
	if m.ExcludeLabelPrefix != "" {
		parts = append(parts, "exclude_label_prefix="+m.ExcludeLabelPrefix)
	}
	if len(m.Types) > 0 {
		parts = append(parts, "types="+strings.Join(m.Types, ","))
	}
	if len(m.Priority) > 0 {
		parts = append(parts, "priority="+strings.Join(m.Priority, ","))
	}
	if m.TitleRegex != "" {
		parts = append(parts, "title~/"+m.TitleRegex+"/")
	}
	if len(parts) == 0 {
		return "matches all cells (no criteria)"
	}
	return "matched on " + strings.Join(parts, " ")
}

// Explain evaluates every route against the cell in priority order and returns
// the full decision trace plus the winning Match (if any). The winning route is
// the first match; later routes are still reported as "not reached".
func (r *Router) Explain(cell model.SourceItem) (Match, bool, []RouteTrace) {
	traces := make([]RouteTrace, 0, len(r.routes))
	var winner Match
	found := false

	for _, route := range r.routes {
		t := RouteTrace{
			RouteID:  route.ID,
			Priority: route.Priority,
			Agent:    route.Agent,
			Worker:   route.Worker,
		}

		if found {
			t.Reason = "not reached (a higher-priority route already matched)"
			traces = append(traces, t)
			continue
		}

		ok, reason := r.evaluate(route, cell)
		t.Matched = ok
		t.Reason = reason

		if ok {
			if route.Agent != "" {
				winner = Match{Route: route}
				t.Selected = true
				found = true
			} else if w, wok := r.workers[route.Worker]; wok {
				winner = Match{Worker: w, Route: route}
				t.Selected = true
				found = true
			} else {
				t.Matched = false
				t.Reason = fmt.Sprintf("matched but worker %q is not defined — skipped", route.Worker)
			}
		}
		traces = append(traces, t)
	}

	return winner, found, traces
}

func toLowerSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[strings.ToLower(s)] = true
	}
	return m
}

func containsInsensitive(ss []string, target string) bool {
	t := strings.ToLower(target)
	for _, s := range ss {
		if strings.ToLower(s) == t {
			return true
		}
	}
	return false
}

// ErrBadRegex is returned when a route's title_regex fails to compile.
type ErrBadRegex struct {
	RouteID string
	Pattern string
	Err     error
}

func (e *ErrBadRegex) Error() string {
	return "route " + e.RouteID + ": invalid title_regex " + e.Pattern + ": " + e.Err.Error()
}
