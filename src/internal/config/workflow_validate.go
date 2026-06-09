package config

import (
	"fmt"
	"time"
)

// supported output_schema property types (JSON Schema subset).
var supportedSchemaTypes = map[string]bool{
	"object":  true,
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"array":   true,
}

// validateWorkflows checks all workflow definitions for structural errors. It is
// called from Config.Validate(). Errors are accumulated (not fail-fast) so a
// single run surfaces every problem.
func (c *Config) validateWorkflows() []error {
	var errs []error
	if len(c.Workflows) == 0 {
		return errs
	}

	agentIDs := c.agentIDSet()
	sourceIDs := c.sourceIDSet()

	// First pass: index workflows by ID and detect ID-level problems so later
	// passes (sub-workflow references) can rely on the map.
	wfByID := make(map[string]WorkflowConfig, len(c.Workflows))
	seen := map[string]bool{}
	for i, wf := range c.Workflows {
		if wf.ID == "" {
			errs = append(errs, fmt.Errorf("workflows[%d]: id is required", i))
			continue
		}
		if seen[wf.ID] {
			errs = append(errs, fmt.Errorf("workflows[%d]: duplicate id %q", i, wf.ID))
		}
		seen[wf.ID] = true
		wfByID[wf.ID] = wf
	}

	for i, wf := range c.Workflows {
		errs = append(errs, c.validateWorkflow(i, wf, agentIDs, sourceIDs, wfByID)...)
	}

	return errs
}

// validateWorkflow validates a single workflow definition.
func (c *Config) validateWorkflow(
	idx int,
	wf WorkflowConfig,
	agentIDs, sourceIDs map[string]bool,
	wfByID map[string]WorkflowConfig,
) []error {
	var errs []error
	ctx := fmt.Sprintf("workflows[%d] %q", idx, wf.ID)

	// Resume policy.
	switch wf.Resume {
	case "", ResumeAllowed, ResumeForbidden, ResumeAuto:
	default:
		errs = append(errs, fmt.Errorf("%s: invalid resume %q (want allowed|forbidden|auto)", ctx, wf.Resume))
	}

	// Result comment mode.
	switch wf.ResultComment {
	case "", ResultCommentOnComplete, ResultCommentPerStep, ResultCommentOff:
	default:
		errs = append(errs, fmt.Errorf("%s: invalid result_comment %q (want on_complete|per_step|off)", ctx, wf.ResultComment))
	}

	// Trigger source reference.
	if wf.Trigger != nil && wf.Trigger.Match.Source != "" && !sourceIDs[wf.Trigger.Match.Source] {
		errs = append(errs, fmt.Errorf("%s: trigger source %q not defined", ctx, wf.Trigger.Match.Source))
	}

	if len(wf.Steps) == 0 {
		errs = append(errs, fmt.Errorf("%s: at least one step is required", ctx))
		return errs
	}

	// Index step IDs (detect duplicates).
	stepIDs := map[string]bool{}
	for i, s := range wf.Steps {
		if s.ID == "" {
			errs = append(errs, fmt.Errorf("%s: steps[%d]: id is required", ctx, i))
			continue
		}
		if stepIDs[s.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate step id %q", ctx, s.ID))
		}
		stepIDs[s.ID] = true
	}

	// Per-step structural validation.
	for i, s := range wf.Steps {
		errs = append(errs, c.validateStep(ctx, i, s, wf, agentIDs, stepIDs, wfByID)...)
	}

	// Graph: depends_on references + cycle detection + back-edge ancestry.
	errs = append(errs, validateStepGraph(ctx, wf, stepIDs)...)

	// resume: auto requires every step to be idempotent.
	if wf.Resume == ResumeAuto {
		for _, s := range wf.Steps {
			if s.StepType() == StepTypeAgent && !s.Idempotent {
				errs = append(errs, fmt.Errorf("%s: resume: auto requires all steps idempotent, but step %q is not", ctx, s.ID))
			}
		}
	}

	return errs
}

// validateStep validates one step according to its type.
func (c *Config) validateStep(
	ctx string,
	i int,
	s StepConfig,
	wf WorkflowConfig,
	agentIDs, stepIDs map[string]bool,
	wfByID map[string]WorkflowConfig,
) []error {
	var errs []error
	sctx := fmt.Sprintf("%s: step %q", ctx, s.ID)

	switch s.StepType() {
	case StepTypeAgent:
		errs = append(errs, c.validateAgentStep(sctx, s, agentIDs, stepIDs)...)
	case StepTypeSplit:
		errs = append(errs, validateSplitStep(sctx, s, stepIDs)...)
	case StepTypeApproval:
		errs = append(errs, validateApprovalStep(sctx, s)...)
	case StepTypeForeach:
		errs = append(errs, c.validateForeachStep(sctx, s, wf, agentIDs)...)
	case StepTypeWorkflow:
		errs = append(errs, validateWorkflowStep(sctx, s, wf, wfByID)...)
	case StepTypeWaitFor:
		errs = append(errs, validateWaitForStep(sctx, s)...)
	default:
		errs = append(errs, fmt.Errorf("%s: unknown step type %q", sctx, s.Type))
	}

	// on_conflict applies to any step type, so validate it here rather than in a
	// per-type validator. It is only meaningful on a wait_for step (the sole
	// producer of a conflict) and otherwise mirrors on_fail (goto + max_retries).
	if s.OnConflict != nil && s.OnConflict.Goto != "" {
		if s.StepType() != StepTypeWaitFor {
			errs = append(errs, fmt.Errorf("%s: on_conflict is only valid on a wait_for step", sctx))
		}
		if !stepIDs[s.OnConflict.Goto] {
			errs = append(errs, fmt.Errorf("%s: on_conflict.goto references unknown step %q", sctx, s.OnConflict.Goto))
		}
		if s.OnConflict.MaxRetries < 1 {
			errs = append(errs, fmt.Errorf("%s: on_conflict.max_retries must be >= 1 when goto is set", sctx))
		}
	}

	return errs
}

// validateAgentStep validates a type: agent step.
func (c *Config) validateAgentStep(sctx string, s StepConfig, agentIDs, stepIDs map[string]bool) []error {
	var errs []error

	if s.Agent == "" {
		errs = append(errs, fmt.Errorf("%s: agent step requires an agent field", sctx))
	} else if !agentIDs[s.Agent] {
		errs = append(errs, fmt.Errorf("%s: agent %q not defined", sctx, s.Agent))
	}

	if s.OutputSchema != nil {
		errs = append(errs, validateOutputSchema(sctx, s.OutputSchema)...)
	}

	switch s.OnMissingOutput {
	case "", OnMissingOutputWarn, OnMissingOutputFail, OnMissingOutputIgnore:
	default:
		errs = append(errs, fmt.Errorf("%s: invalid on_missing_output %q (want warn|fail|ignore)", sctx, s.OnMissingOutput))
	}

	switch s.Spawn {
	case "", SpawnAuto, SpawnAwait:
	default:
		errs = append(errs, fmt.Errorf("%s: invalid spawn %q (want auto|await)", sctx, s.Spawn))
	}

	switch s.Materialize {
	case MaterializeOff, MaterializeSubIssue:
	default:
		errs = append(errs, fmt.Errorf("%s: invalid materialize %q (want sub_issue)", sctx, s.Materialize))
	}

	// materialize: sub_issue creates children via APIARY_SPAWN, which can never
	// complete under spawn: await (a materialized child runs no inline workflow to
	// await) — flag the combination rather than letting it hang at runtime.
	if s.Materialize == MaterializeSubIssue && s.Spawn == SpawnAwait {
		errs = append(errs, fmt.Errorf("%s: materialize: sub_issue is incompatible with spawn: await", sctx))
	}

	// memory.write requires output_schema with matching top-level fields.
	if w := s.MemoryWriteFields(); len(w) > 0 {
		if s.OutputSchema == nil {
			errs = append(errs, fmt.Errorf("%s: memory.write requires output_schema", sctx))
		} else {
			for _, field := range w {
				if _, ok := s.OutputSchema.Properties[field]; !ok {
					errs = append(errs, fmt.Errorf("%s: memory.write field %q not present in output_schema properties", sctx, field))
				}
			}
		}
	}

	// on_pass.next must reference an existing step.
	if s.OnPass != nil && s.OnPass.Next != "" && !stepIDs[s.OnPass.Next] {
		errs = append(errs, fmt.Errorf("%s: on_pass.next references unknown step %q", sctx, s.OnPass.Next))
	}

	// on_fail.goto must reference an existing step and set max_retries >= 1.
	if s.OnFail != nil && s.OnFail.Goto != "" {
		if !stepIDs[s.OnFail.Goto] {
			errs = append(errs, fmt.Errorf("%s: on_fail.goto references unknown step %q", sctx, s.OnFail.Goto))
		}
		if s.OnFail.MaxRetries < 1 {
			errs = append(errs, fmt.Errorf("%s: on_fail.max_retries must be >= 1 when goto is set", sctx))
		}
	}

	return errs
}

// validateSplitStep validates a type: split step.
func validateSplitStep(sctx string, s StepConfig, stepIDs map[string]bool) []error {
	var errs []error

	if s.Agent != "" {
		errs = append(errs, fmt.Errorf("%s: split step must not have an agent field", sctx))
	}
	if len(s.Branches) == 0 {
		errs = append(errs, fmt.Errorf("%s: split step requires at least one branch", sctx))
		return errs
	}

	fallbacks := 0
	for j, b := range s.Branches {
		if b.Goto == "" {
			errs = append(errs, fmt.Errorf("%s: branches[%d]: goto is required", sctx, j))
		} else if !stepIDs[b.Goto] {
			errs = append(errs, fmt.Errorf("%s: branches[%d]: goto references unknown step %q", sctx, j, b.Goto))
		}
		if b.IsFallback() {
			fallbacks++
		}
	}

	// A single-match split (multi: false) must have exactly one fallback.
	if !s.Multi && fallbacks != 1 {
		errs = append(errs, fmt.Errorf("%s: split with multi: false requires exactly one else/fallback branch (found %d)", sctx, fallbacks))
	}

	return errs
}

// validateApprovalStep validates a type: approval step.
func validateApprovalStep(sctx string, s StepConfig) []error {
	var errs []error

	if s.Agent != "" {
		errs = append(errs, fmt.Errorf("%s: approval step must not have an agent field", sctx))
	}
	if s.Message == "" {
		errs = append(errs, fmt.Errorf("%s: approval step requires a message", sctx))
	}
	if s.ResumeOn == nil || s.ResumeOn.IsEmpty() {
		errs = append(errs, fmt.Errorf("%s: approval step requires resume_on with at least one condition", sctx))
	}
	if s.Timeout != "" {
		if _, err := time.ParseDuration(s.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid timeout %q: %v", sctx, s.Timeout, err))
		}
	}

	return errs
}

// validateForeachStep validates a type: foreach step.
func (c *Config) validateForeachStep(sctx string, s StepConfig, wf WorkflowConfig, agentIDs map[string]bool) []error {
	var errs []error

	if s.Items == "" {
		errs = append(errs, fmt.Errorf("%s: foreach step requires items", sctx))
	} else if !foreachItemsResolveToArray(s.Items, wf) {
		errs = append(errs, fmt.Errorf("%s: foreach items %q does not resolve to an array field in a prior step's output_schema", sctx, s.Items))
	}

	if s.Concurrency != 0 && (s.Concurrency < 1 || s.Concurrency > 16) {
		errs = append(errs, fmt.Errorf("%s: foreach concurrency must be between 1 and 16", sctx))
	}
	if s.MaxItems != 0 && (s.MaxItems < 1 || s.MaxItems > 200) {
		errs = append(errs, fmt.Errorf("%s: foreach max_items must be between 1 and 200", sctx))
	}

	if s.Step == nil {
		errs = append(errs, fmt.Errorf("%s: foreach step requires an inner step", sctx))
		return errs
	}
	// The inner step must be an agent step.
	inner := *s.Step
	if inner.Type != "" && inner.Type != StepTypeAgent {
		errs = append(errs, fmt.Errorf("%s: foreach inner step must be type agent, got %q", sctx, inner.Type))
	}
	if inner.Agent == "" {
		errs = append(errs, fmt.Errorf("%s: foreach inner step requires an agent field", sctx))
	} else if !agentIDs[inner.Agent] {
		errs = append(errs, fmt.Errorf("%s: foreach inner step agent %q not defined", sctx, inner.Agent))
	}
	if inner.OutputSchema != nil {
		errs = append(errs, validateOutputSchema(sctx+" (inner step)", inner.OutputSchema)...)
	}

	return errs
}

// validateWorkflowStep validates a type: workflow (sub-workflow) step.
func validateWorkflowStep(sctx string, s StepConfig, parent WorkflowConfig, wfByID map[string]WorkflowConfig) []error {
	var errs []error

	if s.Agent != "" {
		errs = append(errs, fmt.Errorf("%s: workflow step must not have an agent field", sctx))
	}
	if s.Workflow == "" {
		errs = append(errs, fmt.Errorf("%s: workflow step requires a workflow reference", sctx))
		return errs
	}
	if s.Workflow == parent.ID {
		errs = append(errs, fmt.Errorf("%s: workflow step cannot reference its own workflow", sctx))
		return errs
	}
	child, ok := wfByID[s.Workflow]
	if !ok {
		errs = append(errs, fmt.Errorf("%s: workflow %q not defined", sctx, s.Workflow))
		return errs
	}
	// One level of nesting only: the child must not itself contain workflow steps.
	for _, cs := range child.Steps {
		if cs.StepType() == StepTypeWorkflow {
			errs = append(errs, fmt.Errorf("%s: referenced workflow %q contains a sub-workflow step %q; nesting is not allowed", sctx, s.Workflow, cs.ID))
			break
		}
	}

	return errs
}

// validateWaitForStep validates a type: wait_for step.
func validateWaitForStep(sctx string, s StepConfig) []error {
	var errs []error

	if s.Agent != "" {
		errs = append(errs, fmt.Errorf("%s: wait_for step must not have an agent field", sctx))
	}

	if s.WaitFor == nil {
		errs = append(errs, fmt.Errorf("%s: wait_for step requires a wait_for config block", sctx))
		return errs
	}

	cfg := s.WaitFor

	// Validate kind
	if cfg.Kind == "" {
		// Default is "ci", which is valid
	} else if cfg.Kind != "ci" {
		errs = append(errs, fmt.Errorf("%s: wait_for step kind %q not supported (currently only 'ci')", sctx, cfg.Kind))
	}

	// Validate check_interval
	if cfg.CheckInterval != "" {
		if _, err := time.ParseDuration(cfg.CheckInterval); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid wait_for check_interval %q: %v", sctx, cfg.CheckInterval, err))
		}
	}

	// Validate max_duration
	if cfg.MaxDuration != "" {
		if _, err := time.ParseDuration(cfg.MaxDuration); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid wait_for max_duration %q: %v", sctx, cfg.MaxDuration, err))
		}
	}

	return errs
}

// validateOutputSchema validates the supported JSON Schema subset.
func validateOutputSchema(sctx string, sch *OutputSchema) []error {
	var errs []error
	if sch.Type != "object" {
		errs = append(errs, fmt.Errorf("%s: output_schema type must be \"object\", got %q", sctx, sch.Type))
	}
	for name, field := range sch.Properties {
		errs = append(errs, validateSchemaField(fmt.Sprintf("%s: output_schema.%s", sctx, name), field)...)
	}
	for _, req := range sch.Required {
		if _, ok := sch.Properties[req]; !ok {
			errs = append(errs, fmt.Errorf("%s: output_schema required field %q not present in properties", sctx, req))
		}
	}
	return errs
}

// validateSchemaField validates one property type within an output schema.
func validateSchemaField(fctx string, f SchemaField) []error {
	var errs []error
	if !supportedSchemaTypes[f.Type] {
		errs = append(errs, fmt.Errorf("%s: unsupported type %q", fctx, f.Type))
		return errs
	}
	if len(f.Enum) > 0 && f.Type != "string" {
		errs = append(errs, fmt.Errorf("%s: enum is only supported on string fields", fctx))
	}
	if f.Type == "array" {
		if f.Items == nil {
			errs = append(errs, fmt.Errorf("%s: array field requires items", fctx))
		} else {
			errs = append(errs, validateSchemaField(fctx+".items", *f.Items)...)
		}
	}
	if f.Type == "object" {
		for name, sub := range f.Properties {
			errs = append(errs, validateSchemaField(fmt.Sprintf("%s.%s", fctx, name), sub)...)
		}
	}
	return errs
}

// WorkflowWarnings returns non-fatal advisories about workflow definitions. A
// workflow with no trigger that is never referenced as a sub-workflow can never
// run, which is almost always a mistake worth surfacing.
func (c *Config) WorkflowWarnings() []string {
	if len(c.Workflows) == 0 {
		return nil
	}

	referenced := map[string]bool{}
	for _, wf := range c.Workflows {
		for _, s := range wf.Steps {
			if s.StepType() == StepTypeWorkflow && s.Workflow != "" {
				referenced[s.Workflow] = true
			}
		}
	}

	var warnings []string
	for _, wf := range c.Workflows {
		if wf.ID == "" {
			continue
		}
		if wf.Trigger == nil && !referenced[wf.ID] {
			warnings = append(warnings, fmt.Sprintf(
				"workflow %q has no trigger and is not referenced as a sub-workflow; it will never run",
				wf.ID))
		}
	}
	return warnings
}

// agentIDSet returns the set of defined agent IDs.
func (c *Config) agentIDSet() map[string]bool {
	set := make(map[string]bool, len(c.Agents))
	for _, a := range c.Agents {
		set[a.ID] = true
	}
	return set
}

// sourceIDSet returns the set of defined source IDs.
func (c *Config) sourceIDSet() map[string]bool {
	set := make(map[string]bool, len(c.Sources))
	for _, s := range c.Sources {
		set[s.ID] = true
	}
	return set
}
