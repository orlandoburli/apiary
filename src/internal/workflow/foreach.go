package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// defaultForeachMaxItems caps fan-out when the step omits max_items.
const defaultForeachMaxItems = 50

// foreachResult carries the aggregate outcome of a foreach step execution.
type foreachResult struct {
	passed int
	failed int
}

// executeForeachStep runs a foreach step without touching dagRun; it is safe to
// call from a worker goroutine. contribSnap is a snapshot of r.contrib taken on
// the scheduler goroutine before dispatch.
//
// sem is the global concurrency semaphore. When non-nil (concurrent mode), each
// item goroutine acquires a slot from sem; the caller must have released its own
// slot before calling this function. When nil (sequential mode), items run one
// at a time without touching the semaphore.
func (e *Engine) executeForeachStep(
	ctx context.Context, instID string,
	step config.StepConfig, cell model.Cell,
	memSnap []MemoryStep, contribSnap map[string]MemoryStep,
	wfID string, sem chan struct{},
) (StepResult, foreachResult) {
	items, err := resolveItemsFromContrib(step.Items, contribSnap)
	if err != nil {
		aplog.Error("workflow %s: foreach %q: %v", wfID, step.ID, err)
		return StepResult{Success: false, Output: err.Error()}, foreachResult{}
	}

	maxItems := step.MaxItems
	if maxItems == 0 {
		maxItems = defaultForeachMaxItems
	}
	if len(items) > maxItems {
		aplog.Error("workflow %s: foreach %q: %d items exceeds max_items %d",
			wfID, step.ID, len(items), maxItems)
		return StepResult{
			Success: false,
			Output:  fmt.Sprintf("foreach: %d items exceeds max_items %d", len(items), maxItems),
		}, foreachResult{}
	}

	if step.Step == nil {
		aplog.Error("workflow %s: foreach %q: missing inner step", wfID, step.ID)
		return StepResult{Success: false, Output: "foreach: missing inner step"}, foreachResult{}
	}

	as := step.As
	if as == "" {
		as = "item"
	}

	if sem != nil && step.Concurrency > 1 {
		return e.executeForeachConcurrent(ctx, instID, step, cell, memSnap, wfID, sem, items, as)
	}
	return e.executeForeachSequential(ctx, instID, step, cell, memSnap, wfID, items, as)
}

// executeForeachSequential runs foreach items one at a time (original behaviour).
func (e *Engine) executeForeachSequential(
	ctx context.Context, instID string,
	step config.StepConfig, cell model.Cell,
	memSnap []MemoryStep, wfID string,
	items []any, as string,
) (StepResult, foreachResult) {
	var fr foreachResult
	for i, item := range items {
		sub := *step.Step
		sub.ID = fmt.Sprintf("%s[%d]", step.ID, i)
		sub.Type = config.StepTypeAgent
		sub.DependsOn = nil
		sub.Prompt = renderItemTemplate(step.Step.Prompt, as, item)

		res := e.runStep(ctx, instID, sub, cell, memSnap)
		if res.Success {
			fr.passed++
		} else {
			fr.failed++
			if step.FailFast {
				aplog.Info("workflow %s: foreach %q: fail_fast after item %d", wfID, step.ID, i)
				break
			}
		}
	}
	return foreachStepResult(step.ID, fr)
}

// executeForeachConcurrent runs foreach items concurrently, bounded by both
// step.Concurrency (inner cap) and the global semaphore sem. The caller must
// have released its own global slot before calling this function.
func (e *Engine) executeForeachConcurrent(
	ctx context.Context, instID string,
	step config.StepConfig, cell model.Cell,
	memSnap []MemoryStep, wfID string,
	sem chan struct{}, items []any, as string,
) (StepResult, foreachResult) {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		fr      foreachResult
		stopped int32 // 1 when fail_fast triggered
	)

	// innerSem caps how many item goroutines are active simultaneously.
	innerSem := make(chan struct{}, step.Concurrency)

	for i, item := range items {
		if step.FailFast && atomic.LoadInt32(&stopped) != 0 {
			break
		}

		// Throttle goroutine fan-out by the per-foreach concurrency cap.
		innerSem <- struct{}{}

		if step.FailFast && atomic.LoadInt32(&stopped) != 0 {
			<-innerSem
			break
		}

		sub := *step.Step
		sub.ID = fmt.Sprintf("%s[%d]", step.ID, i)
		sub.Type = config.StepTypeAgent
		sub.DependsOn = nil
		sub.Prompt = renderItemTemplate(step.Step.Prompt, as, item)

		wg.Add(1)
		go func(sub config.StepConfig) {
			defer wg.Done()
			defer func() { <-innerSem }()

			sem <- struct{}{} // acquire global slot
			defer func() { <-sem }()

			res := e.runStep(ctx, instID, sub, cell, memSnap)

			mu.Lock()
			if res.Success {
				fr.passed++
			} else {
				fr.failed++
				if step.FailFast {
					atomic.StoreInt32(&stopped, 1)
					aplog.Info("workflow %s: foreach %q: fail_fast triggered by %s",
						wfID, step.ID, sub.ID)
				}
			}
			mu.Unlock()
		}(sub)
	}

	wg.Wait()
	return foreachStepResult(step.ID, fr)
}

// foreachStepResult builds the StepResult from accumulated foreachResult counts.
func foreachStepResult(stepID string, fr foreachResult) (StepResult, foreachResult) {
	allOK := fr.failed == 0
	summary := ""
	if allOK {
		summary = fmt.Sprintf("%s: processed %d item(s)", stepID, fr.passed)
	}
	return StepResult{
		Success: allOK,
		Output:  fmt.Sprintf("foreach: %d passed, %d failed", fr.passed, fr.failed),
		Summary: summary,
	}, fr
}

// failStep marks a step failed in the run state (used by foreach and
// sub-workflow steps that fail before producing a normal step result).
func (r *dagRun) failStep(id string) {
	r.state[id] = stFailed
	r.stepStates[id] = StepState{State: stFailed}
}

// resolveItemsFromContrib resolves a foreach `items` path to an array within a
// prior passed step's structured output, reading from a contrib snapshot.
func resolveItemsFromContrib(path string, contribSnap map[string]MemoryStep) ([]any, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 4 || parts[0] != "steps" || parts[2] != "output" {
		return nil, fmt.Errorf("invalid items path %q (want steps.<id>.output.<field>)", path)
	}
	id := parts[1]
	fields := parts[3:]

	contrib, ok := contribSnap[id]
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
