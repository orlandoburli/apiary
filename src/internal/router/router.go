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
			ID:        wf.ID,
			Priority:  wf.Trigger.Priority,
			Match:     wf.Trigger.Match,
			Agent:     firstAgentStep(wf),
			Exclusive: wf.Trigger.Exclusive,
			Once:      wf.Trigger.Once,
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
	t := targetFromItem(item)
	for _, route := range r.routes {
		if ok, _ := r.evaluateTarget(route, t); ok {
			if m, ok := r.resolveMatch(route); ok {
				return m, true
			}
		}
	}
	return Match{}, false
}

// RouteAll evaluates every trigger against the task in priority order and returns
// all matches — one InternalTask may fan out to several workflows. Evaluation
// stops after the first matched route that is Exclusive, so a terminal trigger
// (e.g. a classifier or catch-all) can claim the task alone instead of fanning
// out alongside lower-priority triggers. Returns nil if nothing matches.
//
// Routing is on the task, not the SourceItem: a source-bound task's routing
// attributes (labels, state, source, type, priority) are kept live by the
// SourceBinder on each poll, so RouteAll observes the same data Route would.
func (r *Router) RouteAll(task model.InternalTask) []Match {
	t := targetFromTask(task)
	var matches []Match
	for _, route := range r.routes {
		ok, _ := r.evaluateTarget(route, t)
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

// ExplainTask returns the same fan-out decision as RouteAll together with a
// stable reason for every route evaluated before an exclusive match.
func (r *Router) ExplainTask(task model.InternalTask) []RouteTrace {
	t := targetFromTask(task)
	traces := make([]RouteTrace, 0, len(r.routes))
	for _, route := range r.routes {
		matched, reason := r.evaluateTarget(route, t)
		selected := false
		if matched {
			_, selected = r.resolveMatch(route)
			if !selected {
				reason = "matched conditions but route has no resolvable agent or worker"
			}
		}
		traces = append(traces, RouteTrace{RouteID: route.ID, Priority: route.Priority, Agent: route.Agent,
			Worker: route.Worker, Matched: matched, Selected: selected, Reason: reason})
		if selected && route.Exclusive {
			break
		}
	}
	return traces
}

// resolveMatch turns a matched route into a Match: agent-based routing is
// preferred (route.Agent), falling back to a defined worker for backward compat.
// Returns false if the route resolves to neither — the caller skips it.
func (r *Router) resolveMatch(route config.RouteConfig) (Match, bool) {
	if route.Agent != "" {
		return Match{Route: route}, true
	}
	if worker, ok := r.workers[route.Worker]; ok {
		return Match{Worker: worker, Route: route}, true
	}
	return Match{}, false
}

// target is the set of attributes a route is evaluated against. Both a SourceItem
// and an InternalTask map onto it (see targetFromItem / targetFromTask), so the
// same matching logic serves Route (SourceItem) and RouteAll (InternalTask).
type target struct {
	sourceID string
	state    string
	labels   []string
	typ      string
	priority string
	title    string
}

func targetFromItem(cell model.SourceItem) target {
	return target{
		sourceID: cell.SourceID,
		state:    cell.State,
		labels:   cell.Labels,
		typ:      cell.Type,
		priority: cell.Priority,
		title:    cell.Title,
	}
}

func targetFromTask(task model.InternalTask) target {
	return target{
		sourceID: task.Metadata.Source,
		state:    task.Metadata.State,
		labels:   task.Metadata.Labels,
		typ:      task.Metadata.Type,
		priority: task.Metadata.Priority,
		title:    task.Title,
	}
}

func (r *Router) matches(route config.RouteConfig, cell model.SourceItem) bool {
	ok, _ := r.evaluate(route, cell)
	return ok
}

// evaluate reports whether a route matches a cell and a human-readable reason.
// On a miss the reason names the first condition that rejected the cell; on a
// hit it summarises which conditions were satisfied.
func (r *Router) evaluate(route config.RouteConfig, cell model.SourceItem) (bool, string) {
	return r.evaluateTarget(route, targetFromItem(cell))
}

// evaluateTarget is the shared matching core. It reports whether a route matches
// the given target and a human-readable reason — on a miss, the first condition
// that rejected it; on a hit, a summary of the satisfied conditions.
func (r *Router) evaluateTarget(route config.RouteConfig, t target) (bool, string) {
	m := route.Match

	if m.Source != "" && t.sourceID != m.Source {
		return false, fmt.Sprintf("source %q != required %q", t.sourceID, m.Source)
	}

	if len(m.States) > 0 && !containsInsensitive(m.States, t.state) {
		return false, fmt.Sprintf("state %q not in %v", t.state, m.States)
	}

	if len(m.Labels) > 0 {
		labels := toLowerSet(t.labels)
		for _, required := range m.Labels {
			if !labels[strings.ToLower(required)] {
				return false, fmt.Sprintf("missing required label %q (cell has %v)", required, t.labels)
			}
		}
	}

	if len(m.ExcludeLabels) > 0 {
		labels := toLowerSet(t.labels)
		for _, excluded := range m.ExcludeLabels {
			if labels[strings.ToLower(excluded)] {
				return false, fmt.Sprintf("has excluded label %q", excluded)
			}
		}
	}

	if m.ExcludeLabelPrefix != "" {
		prefix := strings.ToLower(m.ExcludeLabelPrefix)
		for _, l := range t.labels {
			if strings.HasPrefix(strings.ToLower(l), prefix) {
				return false, fmt.Sprintf("has label %q matching excluded prefix %q", l, m.ExcludeLabelPrefix)
			}
		}
	}

	if len(m.Types) > 0 && !containsInsensitive(m.Types, t.typ) {
		return false, fmt.Sprintf("type %q not in %v", t.typ, m.Types)
	}

	if len(m.Priority) > 0 && !containsInsensitive(m.Priority, t.priority) {
		return false, fmt.Sprintf("priority %q not in %v", t.priority, m.Priority)
	}

	if re, ok := r.regexes[route.ID]; ok {
		if !re.MatchString(t.title) {
			return false, fmt.Sprintf("title %q does not match /%s/", t.title, m.TitleRegex)
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
