package config

import (
	"fmt"
	"strings"
)

// LowerV2Workflow converts a WorkflowConfig whose steps may be written in the
// v2 authored form (nested groups, if:, reject_when:, parallel:, for_each:
// with a steps body) into an equivalent WorkflowConfig using only the DAG IR
// (flat steps with depends_on, condition, fail_when, on_fail.goto, foreach,
// type:workflow). The result is safe to hand directly to the engine.
//
// LowerV2Workflow is idempotent: a workflow that already uses only IR fields
// and contains no v2 authored fields passes through unchanged.
func LowerV2Workflow(wf WorkflowConfig) (WorkflowConfig, error) {
	// Quick exit: no step uses v2 fields → nothing to do.
	if !anyV2Steps(wf.Steps) {
		return wf, nil
	}

	lc := &lowerCtx{
		stepByID: buildStepIndex(wf.Steps),
	}
	lowered, err := lc.lowerSteps(wf.Steps, "", "")
	if err != nil {
		return wf, fmt.Errorf("workflow %q: %w", wf.ID, err)
	}
	out := wf
	out.Steps = lowered
	return out, nil
}

// ──────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────

// lowerCtx holds the mutable state of one lowering pass.
type lowerCtx struct {
	stepByID map[string]StepConfig // original step id → raw v2 node
}

// buildStepIndex builds a flat map of all step ids in the authored tree
// (including nested children) so the lowering pass can resolve references.
func buildStepIndex(steps []StepConfig) map[string]StepConfig {
	m := map[string]StepConfig{}
	var walk func([]StepConfig)
	walk = func(ss []StepConfig) {
		for _, s := range ss {
			if s.ID != "" {
				m[s.ID] = s
			}
			walk(s.SubSteps)
			walk(s.ParallelSteps)
		}
	}
	walk(steps)
	return m
}

// anyV2Steps reports whether any step in the list (or its children) uses a v2
// authored field that requires lowering.
func anyV2Steps(steps []StepConfig) bool {
	for _, s := range steps {
		if s.IsV2Step() {
			return true
		}
		if anyV2Steps(s.SubSteps) || anyV2Steps(s.ParallelSteps) {
			return true
		}
	}
	return false
}

// lowerSteps lowers a list of authored steps into flat IR steps, threading
// implicit depends_on. prevID is the id of the flat step that immediately
// precedes this list (i.e., the list's first child depends on prevID).
// inheritedCondition is an AND-composed condition inherited from parent groups.
func (lc *lowerCtx) lowerSteps(steps []StepConfig, prevID, inheritedCondition string) ([]StepConfig, error) {
	var out []StepConfig
	for _, s := range steps {
		switch {
		case s.ForEachExpr != "":
			// v2 for_each: lower to StepTypeForeach (check before SubSteps since
			// a foreach step also carries SubSteps as its body).
			flat, err := lc.lowerForeachStep(s, prevID, inheritedCondition)
			if err != nil {
				return nil, err
			}
			out = append(out, flat)
			prevID = flat.ID
		case len(s.ParallelSteps) > 0:
			// Parallel: keep as a StepTypeParallel node with lowered children.
			flat, err := lc.lowerParallelStep(s, prevID, inheritedCondition)
			if err != nil {
				return nil, err
			}
			out = append(out, flat)
			prevID = flat.ID
		case len(s.SubSteps) > 0:
			// Group: dissolve into children, applying the group's if: to all.
			groupCond := composeCond(inheritedCondition, lowerExpr(s.If))
			children, err := lc.lowerSteps(s.SubSteps, prevID, groupCond)
			if err != nil {
				return nil, err
			}
			if len(children) > 0 {
				out = append(out, children...)
				prevID = children[len(children)-1].ID
			}
		default:
			// Leaf agent step (or low-level IR step passed through).
			flat, err := lc.lowerLeafStep(s, prevID, inheritedCondition)
			if err != nil {
				return nil, err
			}
			out = append(out, flat)
			prevID = flat.ID
		}
	}
	return out, nil
}

// lowerLeafStep lowers one agent (leaf) step from v2 authored form to IR.
func (lc *lowerCtx) lowerLeafStep(s StepConfig, prevID, inheritedCondition string) (StepConfig, error) {
	out := s

	// Apply implicit sequencing: depend on previous sibling if not overriding.
	if prevID != "" && len(out.DependsOn) == 0 {
		out.DependsOn = []string{prevID}
	}

	// Lower output: alias.
	if out.Output != nil && out.OutputSchema == nil {
		out.OutputSchema = out.Output
	}
	out.Output = nil

	// Lower if: → condition.
	ownCond := lowerExpr(out.If)
	out.Condition = composeCond(inheritedCondition, ownCond)
	out.If = ""

	// Lower reject_when + on_reject → fail_when + on_fail.
	if out.RejectWhen != "" {
		rewritten, err := lc.rewriteExpr(out.RejectWhen, out.ID)
		if err != nil {
			return StepConfig{}, fmt.Errorf("step %q reject_when: %w", out.ID, err)
		}
		out.FailWhen = rewritten
		out.RejectWhen = ""
	}
	if out.OnReject != nil {
		if out.OnFail == nil {
			out.OnFail = &StepOutcome{}
		}
		out.OnFail.Goto = out.OnReject.RestartFrom
		out.OnFail.MaxRetries = out.OnReject.Max
		out.OnReject = nil
	}

	// Auto-wire: add output fields referenced in conditions/fail_when to memory.write.
	lc.autoWireMemory(&out)

	// Lower max: → max_items (for any legacy foreach-style steps that set max).
	if out.Max > 0 && out.MaxItems == 0 {
		out.MaxItems = out.Max
	}
	out.Max = 0

	return out, nil
}

// lowerParallelStep lowers a parallel group step into a StepTypeParallel IR node.
// Children are lowered as a group starting from no prevID (they all depend on
// the parallel step's own predecessor and are run concurrently by the engine).
func (lc *lowerCtx) lowerParallelStep(s StepConfig, prevID, inheritedCondition string) (StepConfig, error) {
	if s.ID == "" {
		return StepConfig{}, fmt.Errorf("parallel step missing required id")
	}
	out := StepConfig{
		ID:   s.ID,
		Name: s.Name,
		Type: StepTypeParallel,
		Join: s.Join,
	}
	if prevID != "" {
		out.DependsOn = []string{prevID}
	}
	ownCond := lowerExpr(s.If)
	out.Condition = composeCond(inheritedCondition, ownCond)

	// Lower children individually (no implicit chaining — they run concurrently).
	loweredChildren := make([]StepConfig, 0, len(s.ParallelSteps))
	for _, child := range s.ParallelSteps {
		flat, err := lc.lowerLeafStep(child, "", "")
		if err != nil {
			return StepConfig{}, fmt.Errorf("parallel step %q child: %w", s.ID, err)
		}
		loweredChildren = append(loweredChildren, flat)
	}
	out.SubSteps = loweredChildren
	return out, nil
}

// lowerForeachStep lowers a v2 for_each step into a StepTypeForeach IR node.
func (lc *lowerCtx) lowerForeachStep(s StepConfig, prevID, inheritedCondition string) (StepConfig, error) {
	if s.ID == "" {
		return StepConfig{}, fmt.Errorf("for_each step missing required id")
	}
	out := StepConfig{
		ID:          s.ID,
		Name:        s.Name,
		Type:        StepTypeForeach,
		As:          s.As,
		Concurrency: s.Concurrency,
		FailFast:    s.FailFast,
	}
	if prevID != "" {
		out.DependsOn = []string{prevID}
	}
	ownCond := lowerExpr(s.If)
	out.Condition = composeCond(inheritedCondition, ownCond)

	// Lower max: → max_items.
	if s.Max > 0 {
		out.MaxItems = s.Max
	} else {
		out.MaxItems = s.MaxItems
	}

	// Lower for_each expression → dot-path for Items.
	itemsPath, stepID, field := parseForEachExpr(s.ForEachExpr)
	out.Items = itemsPath
	// Auto-add the items array field to the referenced step's memory.write.
	if stepID != "" && field != "" {
		lc.ensureMemoryWrite(stepID, field)
	}

	// Lower body steps.
	if len(s.SubSteps) == 1 {
		// Single inner step: use the existing Step pointer.
		inner, err := lc.lowerLeafStep(s.SubSteps[0], "", "")
		if err != nil {
			return StepConfig{}, fmt.Errorf("for_each %q body: %w", s.ID, err)
		}
		out.Step = &inner
	} else if len(s.SubSteps) > 1 {
		// Multi-step body: wrap in an anonymous sub-workflow.
		anonID := s.ID + "-body"
		inner, err := lc.lowerSteps(s.SubSteps, "", "")
		if err != nil {
			return StepConfig{}, fmt.Errorf("for_each %q body: %w", s.ID, err)
		}
		anon := StepConfig{
			ID:   anonID,
			Type: StepTypeWorkflow,
			// SubSteps holds the anon workflow's steps so the engine can run them.
			SubSteps: inner,
		}
		out.Step = &anon
	}

	return out, nil
}

// ──────────────────────────────────────────────────────────────────
// Expression helpers
// ──────────────────────────────────────────────────────────────────

// lowerExpr strips optional ${{ }} delimiters from an authored expression.
func lowerExpr(expr string) string {
	s := strings.TrimSpace(expr)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}

// composeCond composes two condition expressions with AND, handling empty cases.
func composeCond(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "" && b == "":
		return ""
	case a == "":
		return b
	case b == "":
		return a
	default:
		return "(" + a + ") and (" + b + ")"
	}
}

// rewriteExpr rewrites a v2 expression (possibly containing step.field short
// forms like `review.verdict`) to use memory.* accessors, auto-wiring the
// referenced fields into the step's memory.write list.
// currentStepID is the id of the step that owns this expression (for error
// messages; its own fields are also valid accessors via the transient memory).
func (lc *lowerCtx) rewriteExpr(expr string, currentStepID string) (string, error) {
	src := lowerExpr(expr)
	// Walk through IDENT.IDENT pairs where IDENT1 is a known step id.
	result, err := rewriteStepRefs(src, lc.stepByID, lc)
	if err != nil {
		return "", err
	}
	return result, nil
}

// rewriteStepRefs replaces `<stepid>.<field>` with `memory.<field>` and
// records the auto-wire in the step index.
func rewriteStepRefs(src string, stepByID map[string]StepConfig, lc *lowerCtx) (string, error) {
	// Simple tokenizer: split on whitespace-and-operator boundaries, reconstruct.
	// We scan for WORD.WORD sequences where WORD1 is a known step id.
	var buf strings.Builder
	i := 0
	runes := []rune(src)
	for i < len(runes) {
		// Try to read an identifier.
		if isIdentRune(runes[i]) {
			j := i + 1
			for j < len(runes) && isIdentRune(runes[j]) {
				j++
			}
			word1 := string(runes[i:j])
			// Check for a following dot + identifier.
			if j < len(runes) && runes[j] == '.' {
				k := j + 1
				for k < len(runes) && isIdentRune(runes[k]) {
					k++
				}
				if k > j+1 {
					word2 := string(runes[j+1 : k])
					if _, isStep := stepByID[word1]; isStep {
						// Rewrite step.field → memory.field.
						lc.ensureMemoryWrite(word1, word2)
						buf.WriteString("memory.")
						buf.WriteString(word2)
						i = k
						continue
					}
				}
			}
			buf.WriteString(word1)
			i = j
		} else {
			buf.WriteRune(runes[i])
			i++
		}
	}
	return buf.String(), nil
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
}

// autoWireMemory scans the step's condition and fail_when expressions for
// step.field references and adds the fields to the appropriate step's
// memory.write list.
func (lc *lowerCtx) autoWireMemory(s *StepConfig) {
	for _, expr := range []string{s.Condition, s.FailWhen} {
		if expr == "" {
			continue
		}
		// Scan for memory.field patterns that were already rewritten; confirm
		// the field came from a known step and auto-wire that step's output.
		// (Condition/FailWhen at this point already use memory.* from lowerLeafStep.)
		// The auto-wiring happens in rewriteStepRefs for fail_when; here we only
		// handle cases where the user wrote memory.* directly with a known field
		// and the source step must have that field in its output schema.
		// For conditions, scan for any memory.FIELD and make sure the emitting
		// step has it in its memory.write.
		lc.ensureMemoryWriteForExpr(expr)
	}
}

// ensureMemoryWriteForExpr scans an expression for `memory.<field>` accessors
// and attempts to find which step emits that field, adding to its memory.write.
func (lc *lowerCtx) ensureMemoryWriteForExpr(expr string) {
	// Look for memory.FIELD patterns and find which step has that field in its output schema.
	runes := []rune(expr)
	i := 0
	for i < len(runes) {
		if isIdentRune(runes[i]) {
			j := i + 1
			for j < len(runes) && isIdentRune(runes[j]) {
				j++
			}
			word1 := string(runes[i:j])
			if word1 == "memory" && j < len(runes) && runes[j] == '.' {
				k := j + 1
				for k < len(runes) && isIdentRune(runes[k]) {
					k++
				}
				if k > j+1 {
					field := string(runes[j+1 : k])
					// Find which step declares this field in output_schema / output.
					for stepID, step := range lc.stepByID {
						schema := step.OutputSchema
						if schema == nil {
							schema = step.Output
						}
						if schema != nil {
							if _, ok := schema.Properties[field]; ok {
								lc.ensureMemoryWrite(stepID, field)
							}
						}
					}
				}
			}
			i = j
		} else {
			i++
		}
	}
}

// ensureMemoryWrite adds field to stepID's memory.write if not already present,
// updating the step index in place.
func (lc *lowerCtx) ensureMemoryWrite(stepID, field string) {
	s, ok := lc.stepByID[stepID]
	if !ok {
		return
	}
	if s.Memory == nil {
		s.Memory = &MemoryConfig{}
	}
	for _, f := range s.Memory.Write {
		if f == field {
			lc.stepByID[stepID] = s
			return
		}
	}
	s.Memory.Write = append(s.Memory.Write, field)
	lc.stepByID[stepID] = s
}

// parseForEachExpr parses a for_each expression like `${{ design.tasks }}`
// and returns the dot-path (for Items), the step id, and the field name.
// For expressions that are already dot-paths, returns the path as-is.
func parseForEachExpr(expr string) (dotPath, stepID, field string) {
	s := lowerExpr(expr)
	// s is now e.g. "design.tasks" or "steps.design.outputs.tasks"
	parts := strings.SplitN(s, ".", 2)
	if len(parts) == 2 && !strings.HasPrefix(s, "steps.") && !strings.HasPrefix(s, "memory.") {
		return "steps." + parts[0] + ".outputs." + parts[1], parts[0], parts[1]
	}
	// Already a full path.
	return s, "", ""
}

// LowerV2WorkflowInConfig runs LowerV2Workflow on every workflow in the config,
// replacing the steps in-place. Called by Config.Validate so that the engine
// always receives lowered IR.
func LowerV2WorkflowInConfig(cfg *Config) error {
	for i, wf := range cfg.Workflows {
		lowered, err := LowerV2Workflow(wf)
		if err != nil {
			return err
		}
		cfg.Workflows[i] = lowered
	}
	return nil
}
