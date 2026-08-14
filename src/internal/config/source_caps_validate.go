package config

import "fmt"

// capNeed is one workflow feature that requires an optional source capability.
type capNeed struct {
	feature string // human label for the error message
	has     func(SourceCaps) bool
}

// validateSourceCapabilities rejects workflow features that require a write
// capability (set_state, add_labels, approvals, wait_for ci, materialize)
// against sources whose adapter does not implement it — e.g. the read-only
// prometheus alert source. Two rules, mirroring the dependency-wait lint:
//
//   - a workflow whose trigger pins match.source to a specific source errors
//     when THAT source lacks a needed capability (the mismatch is certain);
//   - a workflow with no source pin errors only when NO configured source
//     supports the capability (with mixed sources the item's origin is only
//     known at runtime, where the missing capability degrades to a no-op).
//
// Skipped when the SourceCapabilities hook is not injected (configs built in
// code, isolated tests) or when no sources are configured. Must run after v2
// lowering so nested/parallel steps are flattened into WorkflowConfig.Steps.
func (c *Config) validateSourceCapabilities() []error {
	if SourceCapabilities == nil || len(c.Sources) == 0 {
		return nil
	}

	typeByID := map[string]string{}
	capsByType := map[string]SourceCaps{}
	for _, s := range c.Sources {
		typeByID[s.ID] = s.Type
		if _, ok := capsByType[s.Type]; !ok {
			capsByType[s.Type] = SourceCapabilities(s.Type)
		}
	}

	var errs []error
	for i, w := range c.Workflows {
		wctx := fmt.Sprintf("workflows[%d] %q", i, w.ID)

		var pinned string
		if w.Trigger != nil {
			pinned = w.Trigger.Match.Source
		}

		for _, need := range workflowCapNeeds(w) {
			if pinned != "" {
				srcType, known := typeByID[pinned]
				if !known {
					continue // unknown source id is reported elsewhere
				}
				if !need.has(capsByType[srcType]) {
					errs = append(errs, fmt.Errorf("%s: %s requires a capability that source %q (type %q) does not support — alerts and other read-only sources cannot host it",
						wctx, need.feature, pinned, srcType))
				}
				continue
			}
			supported := false
			for _, caps := range capsByType {
				if need.has(caps) {
					supported = true
					break
				}
			}
			if !supported {
				errs = append(errs, fmt.Errorf("%s: %s requires a capability that no configured source supports",
					wctx, need.feature))
			}
		}
	}
	return errs
}

// workflowCapNeeds collects the capability-requiring features a workflow uses.
func workflowCapNeeds(w WorkflowConfig) []capNeed {
	var needs []capNeed

	addHook := func(scope string, h *OnComplete) {
		if h == nil {
			return
		}
		if h.SetState != "" {
			needs = append(needs, capNeed{scope + ".set_state", func(c SourceCaps) bool { return c.SetState }})
		}
		if len(h.AddLabels) > 0 || h.AssignFromOutput {
			needs = append(needs, capNeed{scope + " label writes (add_labels/assign_from_output)", func(c SourceCaps) bool { return c.AddLabels }})
		}
		if len(h.RemoveLabels) > 0 {
			needs = append(needs, capNeed{scope + ".remove_labels", func(c SourceCaps) bool { return c.RemoveLabels }})
		}
	}
	addHook("on_complete", w.OnComplete)
	addHook("on_fail", w.OnFail)

	for _, s := range flattenSteps(w.Steps) {
		sctx := fmt.Sprintf("step %q", s.ID)
		switch s.StepType() {
		case StepTypeApproval:
			needs = append(needs, capNeed{sctx + " (approval)", func(c SourceCaps) bool { return c.Approvals }})
		case StepTypeWaitFor:
			if s.WaitFor != nil && (s.WaitFor.Kind == "" || s.WaitFor.Kind == WaitKindCI) {
				needs = append(needs, capNeed{sctx + " (wait_for ci)", func(c SourceCaps) bool { return c.CIWait }})
			}
		}
		if s.Materialize == MaterializeSubIssue {
			needs = append(needs, capNeed{sctx + " (materialize: sub_issue)", func(c SourceCaps) bool { return c.SubIssues }})
		}
	}
	return needs
}

// flattenSteps returns every step in declaration order, descending into the
// children of the group nodes the lowering pass produces: a parallel node keeps
// its children in SubSteps and a foreach node keeps its body in Step, so a
// walk over the top-level list alone misses them.
//
// A nested step reaches the very same source as a top-level one, so every
// capability it needs must be linted too. Without this, a `wait_for {kind: ci}`
// inside a parallel: group escaped the check entirely against a source that
// cannot poll CI, and the operator learned about it only from a runtime failure
// (#425).
func flattenSteps(steps []StepConfig) []StepConfig {
	out := make([]StepConfig, 0, len(steps))
	for _, s := range steps {
		out = append(out, s)
		if len(s.SubSteps) > 0 {
			out = append(out, flattenSteps(s.SubSteps)...)
		}
		if s.Step != nil {
			out = append(out, flattenSteps([]StepConfig{*s.Step})...)
		}
	}
	return out
}
