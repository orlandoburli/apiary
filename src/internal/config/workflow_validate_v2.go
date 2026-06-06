package config

import "fmt"

// validateV2Workflows checks v2-specific authoring rules on all workflows
// BEFORE the lowering pass strips the v2 fields. Called from Config.Validate().
func (c *Config) validateV2Workflows() []error {
	var errs []error
	for i, wf := range c.Workflows {
		ctx := fmt.Sprintf("workflows[%d] %q", i, wf.ID)
		errs = append(errs, validateV2Workflow(ctx, wf)...)
	}
	return errs
}

func validateV2Workflow(ctx string, wf WorkflowConfig) []error {
	var errs []error
	if !anyV2Steps(wf.Steps) {
		return errs // pure v1 workflow — nothing to check
	}

	// Rule: no mixing of v2 authored nesting with hand-written v1 primitives in
	// the same workflow. A workflow is either fully v2 or fully v1.
	if hasV1Primitives(wf.Steps) {
		errs = append(errs, fmt.Errorf(
			"%s: mixes v2 authoring (if:/steps:/parallel:/for_each:) with v1 primitives "+
				"(depends_on/branches/goto) in the same workflow — use one or the other",
			ctx))
	}

	errs = append(errs, validateV2Steps(ctx, wf.Steps, nil)...)
	return errs
}

// validateV2Steps recursively validates a list of authored steps.
// siblingsBefore holds the ids of earlier siblings in the same steps: list.
func validateV2Steps(ctx string, steps []StepConfig, siblingsBefore []string) []error {
	var errs []error
	var seen []string // ids in this steps list, in order

	for _, s := range steps {
		stepCtx := fmt.Sprintf("%s: step %q", ctx, s.ID)

		// reject_when without on_reject.
		if s.RejectWhen != "" && s.OnReject == nil {
			errs = append(errs, fmt.Errorf(
				"%s: has reject_when but no on_reject — add on_reject.restart_from and max, or remove reject_when",
				stepCtx))
		}

		// on_reject.restart_from must name an earlier sibling.
		if s.OnReject != nil && s.OnReject.RestartFrom != "" {
			if !containsStr(seen, s.OnReject.RestartFrom) {
				errs = append(errs, fmt.Errorf(
					"%s: on_reject.restart_from %q is not an earlier sibling in the same steps list",
					stepCtx, s.OnReject.RestartFrom))
			}
			if s.OnReject.Max < 1 {
				errs = append(errs, fmt.Errorf(
					"%s: on_reject.max must be ≥ 1, got %d", stepCtx, s.OnReject.Max))
			}
		}

		// Recursively validate group children.
		if len(s.SubSteps) > 0 {
			errs = append(errs, validateV2Steps(stepCtx, s.SubSteps, nil)...)
		}

		// Recursively validate parallel children.
		if len(s.ParallelSteps) > 0 {
			errs = append(errs, validateV2Steps(stepCtx+" (parallel)", s.ParallelSteps, nil)...)
		}

		// Recursively validate foreach body.
		if s.ForEachExpr != "" && len(s.SubSteps) > 0 {
			errs = append(errs, validateV2Steps(stepCtx+" (for_each body)", s.SubSteps, nil)...)
		}

		seen = append(seen, s.ID)
	}
	return errs
}

// hasV1Primitives reports whether any step in the list uses v1-only hand-written
// primitives that conflict with v2 authored nesting.
func hasV1Primitives(steps []StepConfig) bool {
	for _, s := range steps {
		// depends_on is the key v1 primitive — v2 uses implicit ordering instead.
		if len(s.DependsOn) > 0 {
			return true
		}
		// Split branches + goto are v1 split routing.
		if len(s.Branches) > 0 {
			return true
		}
		if len(s.SubSteps) > 0 && hasV1Primitives(s.SubSteps) {
			return true
		}
		if len(s.ParallelSteps) > 0 && hasV1Primitives(s.ParallelSteps) {
			return true
		}
	}
	return false
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
