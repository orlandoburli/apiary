package config

import (
	"fmt"
	"sort"
	"strings"
)

func validateWorkflowContract(ctx string, wf WorkflowConfig) []error {
	var errs []error
	for name, input := range wf.Inputs {
		ictx := fmt.Sprintf("%s: input %q", ctx, name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s: input name must not be empty", ctx))
		}
		if !supportedSchemaTypes[input.Type] {
			errs = append(errs, fmt.Errorf("%s: unsupported type %q", ictx, input.Type))
		} else if input.Default != nil && !WorkflowValueMatchesType(input.Default, input.Type) {
			errs = append(errs, fmt.Errorf("%s: default value does not match type %q", ictx, input.Type))
		}
	}

	stepByID := make(map[string]StepConfig, len(wf.Steps))
	for _, step := range wf.Steps {
		stepByID[step.ID] = step
	}
	for name, output := range wf.Outputs {
		octx := fmt.Sprintf("%s: output %q", ctx, name)
		if name == "" {
			errs = append(errs, fmt.Errorf("%s: output name must not be empty", ctx))
		}
		if !supportedSchemaTypes[output.Type] {
			errs = append(errs, fmt.Errorf("%s: unsupported type %q", octx, output.Type))
		}
		stepID, field, ok := workflowStepOutputRef(output.Value)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: value must reference ${{ steps.<step>.<field> }}", octx))
			continue
		}
		step, exists := stepByID[stepID]
		if !exists {
			errs = append(errs, fmt.Errorf("%s: value references unknown step %q", octx, stepID))
			continue
		}
		if field == "output" {
			if output.Type != "string" {
				errs = append(errs, fmt.Errorf("%s: raw step output must have type string", octx))
			}
			continue
		}
		schema := step.OutputSchema
		if schema == nil {
			schema = step.Output
		}
		if schema == nil || schema.Properties[field].Type == "" {
			errs = append(errs, fmt.Errorf("%s: step %q does not declare output field %q", octx, stepID, field))
			continue
		}
		if output.Type != "" && schema.Properties[field].Type != output.Type {
			errs = append(errs, fmt.Errorf("%s: type %q does not match step %q field %q type %q", octx, output.Type, stepID, field, schema.Properties[field].Type))
		}
	}
	return errs
}

func validateWorkflowReferenceCycles(wfByID map[string]WorkflowConfig) []error {
	state := map[string]int{}
	var stack []string
	var errs []error
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			at := 0
			for at < len(stack) && stack[at] != id {
				at++
			}
			cycle := append(append([]string{}, stack[at:]...), id)
			errs = append(errs, fmt.Errorf("cyclic subworkflow reference: %s", strings.Join(cycle, " -> ")))
			return
		}
		state[id] = 1
		stack = append(stack, id)
		for _, step := range wfByID[id].Steps {
			if step.StepType() == StepTypeWorkflow {
				if _, ok := wfByID[step.Workflow]; ok {
					visit(step.Workflow)
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	ids := make([]string, 0, len(wfByID))
	for id := range wfByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id)
	}
	return errs
}

func workflowStepOutputRef(value string) (stepID, field string, ok bool) {
	s := strings.TrimSpace(value)
	if !strings.HasPrefix(s, "${{") || !strings.HasSuffix(s, "}}") {
		return "", "", false
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "${{"), "}}"))
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != "steps" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func isWorkflowExpression(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}")
}

// WorkflowValueMatchesType checks a runtime or literal value against the small
// JSON-compatible type set used by reusable workflow contracts.
func WorkflowValueMatchesType(value any, typ string) bool {
	switch typ {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return true
		}
	case "integer":
		switch x := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			return x == float64(int64(x))
		case float32:
			return x == float32(int64(x))
		}
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		switch value.(type) {
		case []any, []string:
			return true
		}
	case "object":
		switch value.(type) {
		case map[string]any, map[string]string:
			return true
		}
	}
	return false
}
