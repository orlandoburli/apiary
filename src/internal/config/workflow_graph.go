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
		// DependsOn (v1/lowered explicit edges) and SeqDependsOn (v2 implicit
		// sequential chaining, set by the v2 lowering pass) are both real
		// dependency edges at runtime — the DAG engine treats them identically
		// (see workflow/dag.go). They must be merged here too: a v2 workflow
		// that mixes plain sequential steps with a `parallel:`/`for_each:` step
		// only gets explicit DependsOn on the parallel/foreach step itself,
		// leaving every other step's real dependency invisible to this
		// function if only DependsOn is read — which broke the `len(deps)==0`
		// sequential-order fallback below for the whole workflow as soon as
		// any single step had an explicit DependsOn.
		for _, d := range append(append([]string{}, s.DependsOn...), s.SeqDependsOn...) {
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

	// on_fail.goto / on_conflict.goto back-edges must target an ancestor (a
	// transitive dependency). For v2 workflows (no DependsOn edges), sequential
	// order is the implicit graph: any step earlier in the slice is an ancestor.
	seqOrder := map[string]int{}
	for i, s := range wf.Steps {
		seqOrder[s.ID] = i
	}
	targetsAncestor := func(stepID, goto_ string) bool {
		if len(deps) == 0 {
			return seqOrder[goto_] < seqOrder[stepID]
		}
		return isAncestor(stepID, goto_, deps)
	}
	for _, s := range wf.Steps {
		if s.OnFail != nil && s.OnFail.Goto != "" && stepIDs[s.OnFail.Goto] &&
			!targetsAncestor(s.ID, s.OnFail.Goto) {
			errs = append(errs, fmt.Errorf("%s: step %q on_fail.goto %q must target an ancestor step (a transitive dependency)", ctx, s.ID, s.OnFail.Goto))
		}
		if s.OnConflict != nil && s.OnConflict.Goto != "" && stepIDs[s.OnConflict.Goto] &&
			!targetsAncestor(s.ID, s.OnConflict.Goto) {
			errs = append(errs, fmt.Errorf("%s: step %q on_conflict.goto %q must target an ancestor step (a transitive dependency)", ctx, s.ID, s.OnConflict.Goto))
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
