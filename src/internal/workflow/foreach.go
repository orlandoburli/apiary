package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
)

// defaultForeachMaxItems caps fan-out when the step omits max_items.
const defaultForeachMaxItems = 50

// runForeachStep expands a foreach step over the array resolved from a prior
// step's structured output, running its inner agent step once per item. It
// returns true if the instance must fail (an item failed and no recovery).
func (e *Engine) runForeachStep(ctx context.Context, r *dagRun, step config.StepConfig) bool {
	items, err := resolveItems(step.Items, r)
	if err != nil {
		aplog.Error("workflow %s: foreach %q: %v", r.wf.ID, step.ID, err)
		r.failForeach(step.ID)
		return true
	}

	maxItems := step.MaxItems
	if maxItems == 0 {
		maxItems = defaultForeachMaxItems
	}
	if len(items) > maxItems {
		aplog.Error("workflow %s: foreach %q: %d items exceeds max_items %d",
			r.wf.ID, step.ID, len(items), maxItems)
		r.failForeach(step.ID)
		return true
	}

	if step.Step == nil {
		aplog.Error("workflow %s: foreach %q: missing inner step", r.wf.ID, step.ID)
		r.failForeach(step.ID)
		return true
	}

	as := step.As
	if as == "" {
		as = "item"
	}

	memSteps := r.memSteps() // snapshot: all sub-runs read the same memory
	passed, failed := 0, 0

	for i, item := range items {
		sub := *step.Step
		sub.ID = fmt.Sprintf("%s[%d]", step.ID, i)
		sub.Type = config.StepTypeAgent
		sub.DependsOn = nil
		sub.Prompt = renderItemTemplate(step.Step.Prompt, as, item)

		res := e.runStep(ctx, r.instID, sub, r.cell, memSteps)
		if res.Success {
			passed++
		} else {
			failed++
			if step.FailFast {
				aplog.Info("workflow %s: foreach %q: fail_fast after item %d", r.wf.ID, step.ID, i)
				break
			}
		}
	}

	allOK := failed == 0
	r.state[step.ID] = passFail(allOK)
	// Expose aggregate via the step state: exit_code carries the failed count, so
	// `steps.<id>.exit_code == 0` reads as "all items passed".
	r.stepStates[step.ID] = StepState{
		State:    passFail(allOK),
		ExitCode: failed,
		Output:   fmt.Sprintf("foreach: %d passed, %d failed", passed, failed),
	}
	if allOK {
		r.contrib[step.ID] = MemoryStep{
			StepID:  step.ID,
			Summary: fmt.Sprintf("%s: processed %d item(s)", step.ID, passed),
		}
		r.passedOrder = append(r.passedOrder, step.ID)
		return false
	}
	return true
}

// failForeach marks a foreach step failed in the run state.
func (r *dagRun) failForeach(id string) {
	r.state[id] = stFailed
	r.stepStates[id] = StepState{State: stFailed}
}

// resolveItems resolves a foreach `items` path (steps.<id>.output.<field...>) to
// an array within a prior passed step's structured output.
func resolveItems(path string, r *dagRun) ([]any, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 4 || parts[0] != "steps" || parts[2] != "output" {
		return nil, fmt.Errorf("invalid items path %q (want steps.<id>.output.<field>)", path)
	}
	id := parts[1]
	fields := parts[3:]

	contrib, ok := r.contrib[id]
	if !ok || contrib.Structured == nil {
		return nil, fmt.Errorf("step %q has no structured output to read items from", id)
	}

	var cur any = contrib.Structured
	for _, f := range fields {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("items path %q: %q is not an object", path, f)
		}
		cur, ok = obj[f]
		if !ok {
			return nil, fmt.Errorf("items path %q: field %q not found", path, f)
		}
	}

	arr, ok := cur.([]any)
	if !ok {
		return nil, fmt.Errorf("items path %q does not resolve to an array", path)
	}
	return arr, nil
}

// itemTemplateRe matches {{ var }} and {{ var.field }} with optional whitespace.
var itemTemplateRe = regexp.MustCompile(`\{\{\s*(\w+)(?:\.(\w+))?\s*\}\}`)

// renderItemTemplate substitutes {{ as }} / {{ as.field }} placeholders in a
// prompt with values from the current foreach item.
func renderItemTemplate(tmpl, as string, item any) string {
	if tmpl == "" {
		return ""
	}
	return itemTemplateRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := itemTemplateRe.FindStringSubmatch(match)
		name, field := sub[1], sub[2]
		if name != as {
			return match // not our variable — leave untouched
		}
		if field == "" {
			return renderValue(item)
		}
		if obj, ok := item.(map[string]any); ok {
			if v, ok := obj[field]; ok {
				return renderValue(v)
			}
		}
		return ""
	})
}
