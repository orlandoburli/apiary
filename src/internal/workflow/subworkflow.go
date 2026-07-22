package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/model"
)

// maxSubWorkflowDepth is a runtime backstop for configs that bypass validation.
// Normal recursive references are rejected by the config graph validator.
const maxSubWorkflowDepth = 16

// executeSubWorkflowStep runs a sub-workflow step without touching dagRun; it is
// safe to call from a worker goroutine. memSnap is a snapshot of the parent's
// memory taken on the scheduler goroutine before dispatch.
func (e *Engine) executeSubWorkflowStep(
	ctx context.Context, parentInstID string,
	step config.StepConfig, task model.InternalTask, bindings []model.SourceBinding,
	memSnap []MemoryStep, contribSnap map[string]MemoryStep, depth int, wfID string,
) StepResult {
	started := e.now()
	sr := &db.StepRun{
		ID:                 e.newID("sr"),
		WorkflowInstanceID: parentInstID,
		StepID:             step.ID,
		State:              db.StepStateRunning,
		StartedAt:          &started,
	}
	_ = e.store.CreateStepRun(ctx, sr)
	finish := func(res StepResult) StepResult {
		persistCtx := context.WithoutCancel(ctx)
		finished := e.now()
		sr.FinishedAt = &finished
		sr.Output = res.Output
		sr.Summary = res.Summary
		if res.Success {
			sr.State = db.StepStatePassed
		} else {
			sr.State = db.StepStateFailed
		}
		if len(res.StructuredOutput) > 0 {
			if data, err := json.Marshal(res.StructuredOutput); err == nil {
				sr.StructuredOutput = string(data)
			}
		}
		_ = e.store.UpdateStepRun(persistCtx, sr)
		return res
	}

	if depth >= maxSubWorkflowDepth {
		aplog.Error("workflow %s: step %q: sub-workflow nesting beyond depth %d is not allowed",
			wfID, step.ID, maxSubWorkflowDepth)
		return finish(StepResult{Success: false, Output: "sub-workflow nesting limit exceeded"})
	}

	child := e.findWorkflow(step.Workflow)
	if child == nil {
		aplog.Error("workflow %s: step %q: referenced workflow %q not found", wfID, step.ID, step.Workflow)
		return finish(StepResult{Success: false, Output: fmt.Sprintf("workflow %q not found", step.Workflow)})
	}

	inputs, err := resolveSubworkflowInputs(step, *child, task, bindings, memSnap, contribSnap)
	if err != nil {
		return finish(StepResult{Success: false, Output: err.Error(), Err: err})
	}
	resolvedChild := renderSubworkflowInputs(*child, inputs)
	seed := append([]MemoryStep{}, memSnap...)
	if len(inputs) > 0 {
		keys := make([]string, 0, len(inputs))
		for key := range inputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		seed = append(seed, MemoryStep{StepID: "inputs", WriteFields: keys, Structured: inputs})
	}

	childCtx := ctx
	var cancel context.CancelFunc
	if step.Timeout != "" {
		if timeout, parseErr := time.ParseDuration(step.Timeout); parseErr == nil {
			childCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	childID, outputs, success := e.runChildInstance(childCtx, parentInstID, resolvedChild, task, bindings, seed, depth+1)
	if !success {
		message := fmt.Sprintf("sub-workflow %q failed", child.ID)
		if childCtx.Err() != nil {
			message = fmt.Sprintf("sub-workflow %q canceled: %v", child.ID, childCtx.Err())
		}
		return finish(StepResult{Success: false, Output: message, Err: childCtx.Err()})
	}
	return finish(StepResult{
		Success:          true,
		Output:           childID,
		Summary:          fmt.Sprintf("sub-workflow %q completed", child.ID),
		StructuredOutput: outputs,
	})
}

// runChildInstance creates and runs a linked child workflow instance. It does
// not apply state_lock or result_comment (those belong to the top-level
// instance) and does not apply the child's on_complete/on_fail hooks against the
// shared cell — the child is an isolated pipeline whose only outward signal is
// success/failure.
func (e *Engine) runChildInstance(ctx context.Context, parentInstID string, child config.WorkflowConfig, task model.InternalTask, bindings []model.SourceBinding, seed []MemoryStep, depth int) (string, map[string]any, bool) {
	childID := e.newID("wf")
	cell := sourceItemView(task, bindings)
	inst := &db.WorkflowInstance{
		ID:               childID,
		WorkflowID:       child.ID,
		TaskID:           task.ID,
		CellID:           cell.ID,
		SourceID:         cell.SourceID,
		State:            db.InstanceStateRunning,
		ParentInstanceID: parentInstID,
		CreatedAt:        e.now(),
	}
	if err := e.store.CreateWorkflowInstance(ctx, inst); err != nil {
		aplog.Error("sub-workflow %s: create child instance: %v", child.ID, err)
		return "", nil, false
	}
	if err := e.persistWorkflowSnapshot(ctx, childID, child); err != nil {
		_ = e.store.UpdateWorkflowInstanceState(ctx, childID, db.InstanceStateFailed)
		aplog.Error("sub-workflow %s: persist snapshot: %v", child.ID, err)
		return childID, nil, false
	}

	r := e.initDAG(childID, child, task, bindings, seed, depth)
	outcome := e.driveDAG(ctx, r)
	if ctx.Err() != nil {
		outcome = outcomeFailed
	}
	// A sub-workflow cannot park independently in Phase 4: an approval step inside
	// a child is treated as a failure (unsupported). Top-level approvals are the
	// supported case.
	if outcome == outcomeWaiting {
		aplog.Error("sub-workflow %s: approval steps inside a sub-workflow are not supported", child.ID)
		outcome = outcomeFailed
	}

	finalState := db.InstanceStateDone
	if outcome == outcomeFailed {
		finalState = db.InstanceStateFailed
	}
	outputs := map[string]any{}
	if outcome == outcomeDone {
		var err error
		outputs, err = resolveSubworkflowOutputs(child, r)
		if err != nil {
			aplog.Error("sub-workflow %s: resolve outputs: %v", child.ID, err)
			outcome = outcomeFailed
			finalState = db.InstanceStateFailed
		}
	}
	_ = e.store.UpdateWorkflowInstanceState(context.WithoutCancel(ctx), childID, finalState)
	return childID, outputs, outcome == outcomeDone
}

// findWorkflow looks up a workflow definition by ID in the config.
func (e *Engine) findWorkflow(id string) *config.WorkflowConfig {
	for i := range e.cfg.Workflows {
		if e.cfg.Workflows[i].ID == id {
			return &e.cfg.Workflows[i]
		}
	}
	return nil
}

func resolveSubworkflowInputs(step config.StepConfig, child config.WorkflowConfig, task model.InternalTask, bindings []model.SourceBinding, memSnap []MemoryStep, contrib map[string]MemoryStep) (map[string]any, error) {
	inputs := make(map[string]any, len(child.Inputs))
	for name, declaration := range child.Inputs {
		value, provided := step.With[name]
		if !provided {
			if declaration.Default != nil {
				value = declaration.Default
				provided = true
			} else if declaration.Required {
				return nil, fmt.Errorf("sub-workflow %q: required input %q is missing", child.ID, name)
			}
		}
		if !provided {
			continue
		}
		resolved, err := resolveSubworkflowValue(value, task, bindings, memSnap, contrib)
		if err != nil {
			return nil, fmt.Errorf("sub-workflow %q input %q: %w", child.ID, name, err)
		}
		if !config.WorkflowValueMatchesType(resolved, declaration.Type) {
			return nil, fmt.Errorf("sub-workflow %q input %q: value does not match type %q", child.ID, name, declaration.Type)
		}
		inputs[name] = resolved
	}
	return inputs, nil
}

func resolveSubworkflowValue(value any, task model.InternalTask, bindings []model.SourceBinding, memSnap []MemoryStep, contrib map[string]MemoryStep) (any, error) {
	s, ok := value.(string)
	if !ok {
		return value, nil
	}
	expr := strings.TrimSpace(s)
	if !strings.HasPrefix(expr, "${{") || !strings.HasSuffix(expr, "}}") {
		return value, nil
	}
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expr, "${{"), "}}"))
	parts := strings.Split(expr, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid value expression %q", s)
	}
	switch parts[0] {
	case "task":
		key := strings.Join(parts[1:], ".")
		switch key {
		case "id":
			return task.ID, nil
		case "title":
			return task.Title, nil
		case "description":
			return task.Description, nil
		}
		if value, exists := task.Input[key]; exists {
			return value, nil
		}
	case "cell":
		cell := sourceItemView(task, bindings)
		switch strings.Join(parts[1:], ".") {
		case "id":
			return cell.ID, nil
		case "title":
			return cell.Title, nil
		case "description":
			return cell.Description, nil
		case "source":
			return cell.SourceID, nil
		case "priority":
			return cell.Priority, nil
		case "type":
			return cell.Type, nil
		}
	case "memory":
		key := strings.Join(parts[1:], ".")
		if value, exists := memoryValuesFrom(memSnap)[key]; exists {
			return value, nil
		}
	case "steps":
		if len(parts) == 3 {
			if contribution, exists := contrib[parts[1]]; exists {
				if value, exists := contribution.Structured[parts[2]]; exists {
					return value, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("value expression %q did not resolve", s)
}

func renderSubworkflowInputs(child config.WorkflowConfig, inputs map[string]any) config.WorkflowConfig {
	steps := make([]config.StepConfig, len(child.Steps))
	for i := range child.Steps {
		steps[i] = cloneSubworkflowStep(child.Steps[i])
	}
	child.Steps = steps
	for i := range child.Steps {
		renderStepInputs(&child.Steps[i], inputs)
	}
	return child
}

func cloneSubworkflowStep(step config.StepConfig) config.StepConfig {
	if step.With != nil {
		with := make(map[string]any, len(step.With))
		for key, value := range step.With {
			with[key] = value
		}
		step.With = with
	}
	if step.Env != nil {
		env := make(map[string]string, len(step.Env))
		for key, value := range step.Env {
			env[key] = value
		}
		step.Env = env
	}
	if step.Step != nil {
		inner := cloneSubworkflowStep(*step.Step)
		step.Step = &inner
	}
	if len(step.SubSteps) > 0 {
		children := make([]config.StepConfig, len(step.SubSteps))
		for i := range step.SubSteps {
			children[i] = cloneSubworkflowStep(step.SubSteps[i])
		}
		step.SubSteps = children
	}
	if len(step.ParallelSteps) > 0 {
		children := make([]config.StepConfig, len(step.ParallelSteps))
		for i := range step.ParallelSteps {
			children[i] = cloneSubworkflowStep(step.ParallelSteps[i])
		}
		step.ParallelSteps = children
	}
	return step
}

func renderStepInputs(step *config.StepConfig, inputs map[string]any) {
	step.Prompt = renderInputString(step.Prompt, inputs)
	step.SummaryPrompt = renderInputString(step.SummaryPrompt, inputs)
	step.Message = renderInputString(step.Message, inputs)
	for key, value := range step.With {
		step.With[key] = renderInputValue(value, inputs)
	}
	if step.Step != nil {
		renderStepInputs(step.Step, inputs)
	}
	for i := range step.SubSteps {
		renderStepInputs(&step.SubSteps[i], inputs)
	}
	for i := range step.ParallelSteps {
		renderStepInputs(&step.ParallelSteps[i], inputs)
	}
}

func renderInputValue(value any, inputs map[string]any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		expr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "${{"), "}}"))
		parts := strings.Split(expr, ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			if value, exists := inputs[parts[1]]; exists {
				return value
			}
		}
	}
	return renderInputString(s, inputs)
}

func renderInputString(value string, inputs map[string]any) string {
	for name, input := range inputs {
		value = strings.ReplaceAll(value, "${{ inputs."+name+" }}", renderValue(input))
		value = strings.ReplaceAll(value, "${{inputs."+name+"}}", renderValue(input))
	}
	return value
}

func resolveSubworkflowOutputs(child config.WorkflowConfig, run *dagRun) (map[string]any, error) {
	outputs := make(map[string]any, len(child.Outputs))
	for name, declaration := range child.Outputs {
		expr := strings.TrimSpace(declaration.Value)
		expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expr, "${{"), "}}"))
		parts := strings.Split(expr, ".")
		if len(parts) != 3 || parts[0] != "steps" {
			return nil, fmt.Errorf("output %q has invalid value expression %q", name, declaration.Value)
		}
		var value any
		var exists bool
		if parts[2] == "output" {
			state, ok := run.stepStates[parts[1]]
			if ok {
				value, exists = state.Output, true
			}
		} else if contribution, ok := run.contrib[parts[1]]; ok {
			value, exists = contribution.Structured[parts[2]]
		}
		if !exists {
			return nil, fmt.Errorf("output %q could not resolve %q", name, declaration.Value)
		}
		if !config.WorkflowValueMatchesType(value, declaration.Type) {
			return nil, fmt.Errorf("output %q does not match type %q", name, declaration.Type)
		}
		outputs[name] = value
	}
	return outputs, nil
}
