package config

import (
	"fmt"
	"strings"
)

// validateStepGraph validates the depends_on graph of a workflow: reference
// integrity, acyclicity, and on_fail.goto back-edge ancestry.
func validateStepGraph(ctx string, wf WorkflowConfig, stepIDs map[string]bool) []error {
	var errs []error

	adj := map[string][]string{}  // dependency -> dependents (forward edges)
	deps := map[string][]string{} // step -> its direct dependencies

	for _, s := range wf.Steps {
		for _, d := range s.DependsOn {
			if !stepIDs[d] {
				errs = append(errs, fmt.Errorf("%s: step %q depends_on unknown step %q", ctx, s.ID, d))
				continue
			}
			if d == s.ID {
				errs = append(errs, fmt.Errorf("%s: step %q depends on itself", ctx, s.ID))
				continue
			}
			adj[d] = append(adj[d], s.ID)
			deps[s.ID] = append(deps[s.ID], d)
		}
	}

	if cyc := findCycle(wf, adj); cyc != "" {
		errs = append(errs, fmt.Errorf("%s: dependency cycle detected involving step %q", ctx, cyc))
	}

	// on_fail.goto back-edges must target an ancestor (a transitive dependency).
	for _, s := range wf.Steps {
		if s.OnFail == nil || s.OnFail.Goto == "" || !stepIDs[s.OnFail.Goto] {
			continue
		}
		if !isAncestor(s.ID, s.OnFail.Goto, deps) {
			errs = append(errs, fmt.Errorf("%s: step %q on_fail.goto %q must target an ancestor step (a transitive dependency)", ctx, s.ID, s.OnFail.Goto))
		}
	}

	return errs
}

// findCycle returns the ID of a step participating in a dependency cycle, or ""
// when the depends_on graph is acyclic. Standard white/gray/black DFS.
func findCycle(wf WorkflowConfig, adj map[string][]string) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	for _, s := range wf.Steps {
		color[s.ID] = white
	}

	var visit func(node string) string
	visit = func(node string) string {
		color[node] = gray
		for _, next := range adj[node] {
			switch color[next] {
			case gray:
				return next
			case white:
				if c := visit(next); c != "" {
					return c
				}
			}
		}
		color[node] = black
		return ""
	}

	for _, s := range wf.Steps {
		if color[s.ID] == white {
			if c := visit(s.ID); c != "" {
				return c
			}
		}
	}
	return ""
}

// isAncestor reports whether candidate is a transitive dependency of node,
// following the deps (step -> dependencies) relation. A visited set guards
// against cycles so this terminates even on malformed graphs.
func isAncestor(node, candidate string, deps map[string][]string) bool {
	visited := map[string]bool{}
	queue := append([]string{}, deps[node]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == candidate {
			return true
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		queue = append(queue, deps[cur]...)
	}
	return false
}

// foreachItemsResolveToArray reports whether the items expression
// (e.g. "steps.find-issues.output.issues") resolves to an array-typed field in
// the referenced step's output_schema within the same workflow.
func foreachItemsResolveToArray(items string, wf WorkflowConfig) bool {
	parts := strings.Split(items, ".")
	if len(parts) < 4 || parts[0] != "steps" || parts[2] != "output" {
		return false
	}
	stepID := parts[1]
	fieldPath := parts[3:]

	var schema *OutputSchema
	for _, s := range wf.Steps {
		if s.ID == stepID {
			schema = s.OutputSchema
			break
		}
	}
	if schema == nil {
		return false
	}

	props := schema.Properties
	var field SchemaField
	for i, p := range fieldPath {
		f, ok := props[p]
		if !ok {
			return false
		}
		field = f
		if i < len(fieldPath)-1 {
			if f.Type != "object" {
				return false
			}
			props = f.Properties
		}
	}
	return field.Type == "array"
}
