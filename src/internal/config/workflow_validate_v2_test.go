package config

import (
	"strings"
	"testing"
)

func TestValidateV2_RejectWhenWithoutOnReject(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "review", Agent: "a", RejectWhen: `${{ memory.verdict == "rejected" }}`},
		},
	}}
	errs := c.validateV2Workflows()
	if !anyError(errs, "on_reject") {
		t.Errorf("expected on_reject error, got: %v", errs)
	}
}

func TestValidateV2_RejectWhenWithOnReject_OK(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "impl", Agent: "a"},
			{ID: "review", Agent: "a",
				RejectWhen: `${{ memory.verdict == "rejected" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "impl", Max: 2}},
		},
	}}
	errs := c.validateV2Workflows()
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateV2_RestartFromEarlierSibling(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "impl", Agent: "a"},
			{ID: "review", Agent: "a",
				RejectWhen: `${{ memory.v == "no" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "impl", Max: 2}},
		},
	}}
	errs := c.validateV2Workflows()
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateV2_RestartFromNotEarlierSibling(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "review", Agent: "a",
				RejectWhen: `${{ memory.v == "no" }}`,
				// "qa" doesn't exist yet as an earlier sibling
				OnReject: &OnRejectConfig{RestartFrom: "qa", Max: 2}},
			{ID: "qa", Agent: "a"},
		},
	}}
	errs := c.validateV2Workflows()
	if !anyError(errs, "earlier sibling") {
		t.Errorf("expected earlier-sibling error, got: %v", errs)
	}
}

func TestValidateV2_OnRejectMaxZero(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "impl", Agent: "a"},
			{ID: "review", Agent: "a",
				RejectWhen: `${{ memory.v == "no" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "impl", Max: 0}},
		},
	}}
	errs := c.validateV2Workflows()
	if !anyError(errs, "max") {
		t.Errorf("expected max error, got: %v", errs)
	}
}

func TestValidateV2_MixV1V2_Rejected(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag", If: `${{ cell.priority == "high" }}`},
			{ID: "b", Agent: "ag", DependsOn: []string{"a"}}, // v1 depends_on mixed in
		},
	}}
	errs := c.validateV2Workflows()
	if !anyError(errs, "mixes v2") {
		t.Errorf("expected v2+v1 mix error, got: %v", errs)
	}
}

func TestValidateV2_PureV1_NoErrors(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag"},
			{ID: "b", Agent: "ag", DependsOn: []string{"a"}},
		},
	}}
	errs := c.validateV2Workflows()
	if len(errs) != 0 {
		t.Errorf("pure v1 workflow should produce no v2 validation errors, got: %v", errs)
	}
}

func TestValidateV2_PureV2_NoErrors(t *testing.T) {
	c := minimalConfig()
	c.Workflows = []WorkflowConfig{{
		ID: "w",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag"},
			{ID: "b", Agent: "ag", If: `${{ cell.priority == "high" }}`},
		},
	}}
	errs := c.validateV2Workflows()
	if len(errs) != 0 {
		t.Errorf("pure v2 workflow should produce no v2 validation errors, got: %v", errs)
	}
}

// minimalConfig returns a minimal valid config for testing validation helpers.
func minimalConfig() *Config {
	return &Config{
		Version: "1",
		Agents:  []AgentConfig{{ID: "a", Model: "m"}},
	}
}

func anyError(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}
