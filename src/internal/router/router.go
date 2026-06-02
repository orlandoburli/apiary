// Package router evaluates routing rules against a Cell and returns the
// first matching worker configuration.
package router

import (
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

// Router evaluates priority-ordered routing rules against incoming Cells.
type Router struct {
	routes  []config.RouteConfig  // sorted by priority ascending
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

	routes := make([]config.RouteConfig, len(cfg.Routes))
	copy(routes, cfg.Routes)
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

// Route evaluates all rules against the Cell and returns the first match.
// Returns (zero, false) if no rule matches.
func (r *Router) Route(cell model.Cell) (Match, bool) {
	for _, route := range r.routes {
		if r.matches(route, cell) {
			worker, ok := r.workers[route.Worker]
			if !ok {
				continue
			}
			return Match{Worker: worker, Route: route}, true
		}
	}
	return Match{}, false
}

func (r *Router) matches(route config.RouteConfig, cell model.Cell) bool {
	m := route.Match

	if m.Source != "" && cell.SourceID != m.Source {
		return false
	}

	if len(m.Labels) > 0 {
		cellLabels := toLowerSet(cell.Labels)
		for _, required := range m.Labels {
			if !cellLabels[strings.ToLower(required)] {
				return false
			}
		}
	}

	if len(m.Types) > 0 && !containsInsensitive(m.Types, cell.Type) {
		return false
	}

	if len(m.Priority) > 0 && !containsInsensitive(m.Priority, cell.Priority) {
		return false
	}

	if re, ok := r.regexes[route.ID]; ok {
		if !re.MatchString(cell.Title) {
			return false
		}
	}

	return true
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
