package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// boolPtr returns a pointer to b, for MemoryConfig.Read.
func boolPtr(b bool) *bool { return &b }

// baseWorkflowConfig returns a Config with one source and two agents, ready to
// have a Workflows slice attached for testing.
func baseWorkflowConfig() *config.Config {
	return &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Agents: []config.AgentConfig{
			{ID: "architect", Model: "claude-opus-4-8"},
			{ID: "backend-dev", Model: "claude-sonnet-4-6"},
		},
	}
}

// assertNoError fails if the config produces any validation error.
func assertNoError(t *testing.T, cfg *config.Config) {
	t.Helper()
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("expected no validation errors, got: %v", errs)
	}
}

func TestWorkflow_ValidFullPipeline(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{
			ID:     "feature-development",
			Resume: config.ResumeAllowed,
			Trigger: &config.TriggerConfig{
				Priority: 10,
				Match:    config.RouteMatch{Source: "src-1", Labels: []string{"feature"}},
			},
			Steps: []config.StepConfig{
				{
					ID:    "plan",
					Agent: "architect",
					OutputSchema: &config.OutputSchema{
						Type: "object",
						Properties: map[string]config.SchemaField{
							"complexity": {Type: "string", Enum: []string{"low", "high"}},
							"issues": {Type: "array", Items: &config.SchemaField{
								Type: "object",
								Properties: map[string]config.SchemaField{
									"file": {Type: "string"},
								},
							}},
						},
						Required: []string{"complexity"},
					},
					Memory: &config.MemoryConfig{Write: []string{"complexity"}},
				},
				{
					ID:        "route",
					Type:      config.StepTypeSplit,
					DependsOn: []string{"plan"},
					Branches: []config.SplitBranch{
						{If: "memory.complexity == 'high'", Goto: "implement"},
						{Else: true, Goto: "implement"},
					},
				},
				{
					ID:        "implement",
					Agent:     "backend-dev",
					DependsOn: []string{"plan"},
				},
				{
					ID:        "review",
					Agent:     "backend-dev",
					DependsOn: []string{"implement"},
					OnFail:    &config.StepOutcome{Goto: "implement", MaxRetries: 2},
				},
				{
					ID:          "fix-each",
					Type:        config.StepTypeForeach,
					DependsOn:   []string{"plan"},
					Items:       "steps.plan.output.issues",
					As:          "issue",
					Concurrency: 4,
					MaxItems:    20,
					Step: &config.StepConfig{
						Agent: "backend-dev",
					},
				},
				{
					ID:        "approval",
					Type:      config.StepTypeApproval,
					DependsOn: []string{"review"},
					Message:   "Approve?",
					ResumeOn:  &config.ApprovalTrigger{CommentContains: "approve"},
					Timeout:   "48h",
				},
			},
		},
	}
	assertNoError(t, cfg)
}

func TestWorkflow_DuplicateID(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "dup", Steps: []config.StepConfig{{ID: "s1", Agent: "architect"}}},
		{ID: "dup", Steps: []config.StepConfig{{ID: "s1", Agent: "architect"}}},
	}
	assertError(t, cfg, "duplicate id")
}

func TestWorkflow_IDConflictsWithRoute(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Routes = []config.RouteConfig{
		{ID: "shared", Priority: 1, Agent: "architect", Match: config.RouteMatch{Source: "src-1"}},
	}
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "shared", Steps: []config.StepConfig{{ID: "s1", Agent: "architect"}}},
	}
	assertError(t, cfg, "conflicts with a route")
}

func TestWorkflow_InvalidResume(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Resume: "maybe", Steps: []config.StepConfig{{ID: "s1", Agent: "architect"}}},
	}
	assertError(t, cfg, "invalid resume")
}

func TestWorkflow_InvalidResultComment(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", ResultComment: "always", Steps: []config.StepConfig{{ID: "s1", Agent: "architect"}}},
	}
	assertError(t, cfg, "invalid result_comment")
}

func TestWorkflow_TriggerSourceNotDefined(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{
			ID:      "wf",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "ghost"}},
			Steps:   []config.StepConfig{{ID: "s1", Agent: "architect"}},
		},
	}
	assertError(t, cfg, "trigger source")
}

func TestWorkflow_NoSteps(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{{ID: "wf"}}
	assertError(t, cfg, "at least one step is required")
}

func TestWorkflow_DuplicateStepID(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s1", Agent: "architect"},
			{ID: "s1", Agent: "backend-dev"},
		}},
	}
	assertError(t, cfg, "duplicate step id")
}

func TestWorkflow_DependsOnUnknownStep(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s1", Agent: "architect", DependsOn: []string{"ghost"}},
		}},
	}
	assertError(t, cfg, "depends_on unknown step")
}

func TestWorkflow_DependencyCycle(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "a", Agent: "architect", DependsOn: []string{"b"}},
			{ID: "b", Agent: "backend-dev", DependsOn: []string{"a"}},
		}},
	}
	assertError(t, cfg, "cycle detected")
}

func TestWorkflow_AgentStepMissingAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{{ID: "s1"}}},
	}
	assertError(t, cfg, "agent step requires an agent")
}

func TestWorkflow_AgentStepUnknownAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{{ID: "s1", Agent: "ghost"}}},
	}
	assertError(t, cfg, "agent \"ghost\" not defined")
}

func TestWorkflow_MemoryWriteWithoutSchema(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s1", Agent: "architect", Memory: &config.MemoryConfig{Write: []string{"x"}}},
		}},
	}
	assertError(t, cfg, "memory.write requires output_schema")
}

func TestWorkflow_MemoryWriteFieldNotInSchema(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "s1",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type:       "object",
					Properties: map[string]config.SchemaField{"complexity": {Type: "string"}},
				},
				Memory: &config.MemoryConfig{Write: []string{"missing"}},
			},
		}},
	}
	assertError(t, cfg, "not present in output_schema")
}

func TestWorkflow_OnFailGotoUnknownStep(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s1", Agent: "architect", OnFail: &config.StepOutcome{Goto: "ghost", MaxRetries: 1}},
		}},
	}
	assertError(t, cfg, "on_fail.goto references unknown step")
}

func TestWorkflow_OnFailMissingMaxRetries(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "a", Agent: "architect"},
			{ID: "b", Agent: "backend-dev", DependsOn: []string{"a"}, OnFail: &config.StepOutcome{Goto: "a"}},
		}},
	}
	assertError(t, cfg, "on_fail.max_retries must be >= 1")
}

func TestWorkflow_OnFailGotoNotAncestor(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "a", Agent: "architect"},
			{ID: "b", Agent: "backend-dev"},
			// b does not depend on a, so goto a is not an ancestor back-edge.
			{ID: "c", Agent: "architect", DependsOn: []string{"b"}, OnFail: &config.StepOutcome{Goto: "a", MaxRetries: 1}},
		}},
	}
	assertError(t, cfg, "must target an ancestor")
}

func TestWorkflow_OnPassNextUnknownStep(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s1", Agent: "architect", OnPass: &config.StepNext{Next: "ghost"}},
		}},
	}
	assertError(t, cfg, "on_pass.next references unknown step")
}

func TestWorkflow_SplitMustNotHaveAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "target", Agent: "architect"},
			{ID: "s", Type: config.StepTypeSplit, Agent: "architect", Branches: []config.SplitBranch{
				{Else: true, Goto: "target"},
			}},
		}},
	}
	assertError(t, cfg, "split step must not have an agent")
}

func TestWorkflow_SplitNoBranches(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeSplit},
		}},
	}
	assertError(t, cfg, "requires at least one branch")
}

func TestWorkflow_SplitSingleMatchRequiresOneFallback(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "target", Agent: "architect"},
			{ID: "s", Type: config.StepTypeSplit, Branches: []config.SplitBranch{
				{If: "cell.priority == 'high'", Goto: "target"},
			}},
		}},
	}
	assertError(t, cfg, "exactly one else/fallback")
}

func TestWorkflow_SplitBranchGotoUnknown(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeSplit, Branches: []config.SplitBranch{
				{Else: true, Goto: "ghost"},
			}},
		}},
	}
	assertError(t, cfg, "goto references unknown step")
}

func TestWorkflow_SplitMultiAllowsNoFallback(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "a", Agent: "architect"},
			{ID: "b", Agent: "backend-dev"},
			{ID: "s", Type: config.StepTypeSplit, Multi: true, Branches: []config.SplitBranch{
				{If: "cell.labels contains 'x'", Goto: "a"},
				{If: "cell.labels contains 'y'", Goto: "b"},
			}},
		}},
	}
	assertNoError(t, cfg)
}

func TestWorkflow_ApprovalMustNotHaveAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeApproval, Agent: "architect", Message: "ok",
				ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"}},
		}},
	}
	assertError(t, cfg, "approval step must not have an agent")
}

func TestWorkflow_ApprovalMissingMessage(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeApproval, ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"}},
		}},
	}
	assertError(t, cfg, "requires a message")
}

func TestWorkflow_ApprovalMissingResumeOn(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeApproval, Message: "ok"},
		}},
	}
	assertError(t, cfg, "requires resume_on")
}

func TestWorkflow_ApprovalInvalidTimeout(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeApproval, Message: "ok",
				ResumeOn: &config.ApprovalTrigger{CommentContains: "approve"}, Timeout: "soon"},
		}},
	}
	assertError(t, cfg, "invalid timeout")
}

func TestWorkflow_ForeachMissingItems(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeForeach, Step: &config.StepConfig{Agent: "architect"}},
		}},
	}
	assertError(t, cfg, "foreach step requires items")
}

func TestWorkflow_ForeachItemsNotArray(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type:       "object",
					Properties: map[string]config.SchemaField{"name": {Type: "string"}},
				},
			},
			{ID: "s", Type: config.StepTypeForeach, DependsOn: []string{"plan"},
				Items: "steps.plan.output.name", Step: &config.StepConfig{Agent: "architect"}},
		}},
	}
	assertError(t, cfg, "does not resolve to an array")
}

func TestWorkflow_ForeachConcurrencyOutOfRange(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"items": {Type: "array", Items: &config.SchemaField{Type: "string"}},
					},
				},
			},
			{ID: "s", Type: config.StepTypeForeach, DependsOn: []string{"plan"},
				Items: "steps.plan.output.items", Concurrency: 99,
				Step: &config.StepConfig{Agent: "architect"}},
		}},
	}
	assertError(t, cfg, "concurrency must be between 1 and 16")
}

func TestWorkflow_ForeachMaxItemsOutOfRange(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"items": {Type: "array", Items: &config.SchemaField{Type: "string"}},
					},
				},
			},
			{ID: "s", Type: config.StepTypeForeach, DependsOn: []string{"plan"},
				Items: "steps.plan.output.items", MaxItems: 9999,
				Step: &config.StepConfig{Agent: "architect"}},
		}},
	}
	assertError(t, cfg, "max_items must be between 1 and 200")
}

func TestWorkflow_ForeachInnerStepNotAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"items": {Type: "array", Items: &config.SchemaField{Type: "string"}},
					},
				},
			},
			{ID: "s", Type: config.StepTypeForeach, DependsOn: []string{"plan"},
				Items: "steps.plan.output.items",
				Step:  &config.StepConfig{Type: config.StepTypeSplit, Agent: "architect"}},
		}},
	}
	assertError(t, cfg, "foreach inner step must be type agent")
}

func TestWorkflow_ForeachInnerMissingAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				OutputSchema: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"items": {Type: "array", Items: &config.SchemaField{Type: "string"}},
					},
				},
			},
			{ID: "s", Type: config.StepTypeForeach, DependsOn: []string{"plan"},
				Items: "steps.plan.output.items", Step: &config.StepConfig{}},
		}},
	}
	assertError(t, cfg, "foreach inner step requires an agent")
}

func TestWorkflow_StepMustNotHaveAgent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "sub", Steps: []config.StepConfig{{ID: "x", Agent: "architect"}}},
		{ID: "main", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow, Agent: "architect", Workflow: "sub"},
		}},
	}
	assertError(t, cfg, "workflow step must not have an agent")
}

func TestWorkflow_StepMissingWorkflowRef(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "main", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow},
		}},
	}
	assertError(t, cfg, "requires a workflow reference")
}

func TestWorkflow_StepSelfReference(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "main", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow, Workflow: "main"},
		}},
	}
	assertError(t, cfg, "cannot reference its own workflow")
}

func TestWorkflow_StepUnknownWorkflowRef(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "main", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow, Workflow: "ghost"},
		}},
	}
	assertError(t, cfg, "workflow \"ghost\" not defined")
}

func TestWorkflow_SubWorkflowNestingNotAllowed(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "leaf", Steps: []config.StepConfig{{ID: "x", Agent: "architect"}}},
		{ID: "mid", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow, Workflow: "leaf"},
		}},
		{ID: "top", Steps: []config.StepConfig{
			{ID: "s", Type: config.StepTypeWorkflow, Workflow: "mid"},
		}},
	}
	assertError(t, cfg, "nesting is not allowed")
}

func TestWorkflow_OutputSchemaTypeNotObject(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", OutputSchema: &config.OutputSchema{Type: "string"}},
		}},
	}
	assertError(t, cfg, "type must be \"object\"")
}

func TestWorkflow_OutputSchemaUnsupportedType(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"x": {Type: "decimal"}},
			}},
		}},
	}
	assertError(t, cfg, "unsupported type")
}

func TestWorkflow_OutputSchemaRequiredNotInProperties(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"x": {Type: "string"}},
				Required:   []string{"y"},
			}},
		}},
	}
	assertError(t, cfg, "required field \"y\" not present")
}

func TestWorkflow_OutputSchemaEnumOnNonString(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"x": {Type: "number", Enum: []string{"1", "2"}}},
			}},
		}},
	}
	assertError(t, cfg, "enum is only supported on string")
}

func TestWorkflow_OutputSchemaArrayWithoutItems(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", OutputSchema: &config.OutputSchema{
				Type:       "object",
				Properties: map[string]config.SchemaField{"x": {Type: "array"}},
			}},
		}},
	}
	assertError(t, cfg, "array field requires items")
}

func TestWorkflow_ResumeAutoRequiresIdempotent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Resume: config.ResumeAuto, Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", Idempotent: false},
		}},
	}
	assertError(t, cfg, "resume: auto requires all steps idempotent")
}

func TestWorkflow_ResumeAutoAllIdempotent(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Resume: config.ResumeAuto, Steps: []config.StepConfig{
			{ID: "s", Agent: "architect", Idempotent: true},
		}},
	}
	assertNoError(t, cfg)
}

func TestWorkflow_UnknownStepType(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{{ID: "s", Type: "frobnicate"}}},
	}
	assertError(t, cfg, "unknown step type")
}

func TestWorkflow_MemoryReadDefaultsTrue(t *testing.T) {
	s := config.StepConfig{ID: "s", Agent: "a"}
	if !s.MemoryReadEnabled() {
		t.Error("expected MemoryReadEnabled() to default to true")
	}
	s.Memory = &config.MemoryConfig{Read: boolPtr(false)}
	if s.MemoryReadEnabled() {
		t.Error("expected MemoryReadEnabled() to be false when read: false")
	}
}

func TestWorkflow_WarnsOnOrphanWorkflow(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		// no trigger, not referenced anywhere → orphan
		{ID: "orphan", Steps: []config.StepConfig{{ID: "s", Agent: "architect"}}},
	}
	warnings := cfg.WorkflowWarnings()
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !containsStr(warnings[0], "will never run") {
		t.Errorf("unexpected warning text: %q", warnings[0])
	}
}

func TestWorkflow_NoWarnForTriggeredOrSubWorkflow(t *testing.T) {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{
			ID:      "triggered",
			Trigger: &config.TriggerConfig{Match: config.RouteMatch{Source: "src-1"}},
			Steps: []config.StepConfig{
				{ID: "call", Type: config.StepTypeWorkflow, Workflow: "sub"},
			},
		},
		// referenced as a sub-workflow → not an orphan despite having no trigger
		{ID: "sub", Steps: []config.StepConfig{{ID: "s", Agent: "architect"}}},
	}
	if w := cfg.WorkflowWarnings(); len(w) != 0 {
		t.Errorf("expected no warnings, got: %v", w)
	}
}

func TestWorkflow_NoWorkflowsStillValid(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Sources: []config.SourceConfig{{ID: "src-1", Type: "plane"}},
		Agents:  []config.AgentConfig{{ID: "a-1", Model: "claude-sonnet-4-6"}},
		Routes: []config.RouteConfig{
			{ID: "r-1", Priority: 1, Agent: "a-1", Match: config.RouteMatch{Source: "src-1"}},
		},
	}
	assertNoError(t, cfg)
}
