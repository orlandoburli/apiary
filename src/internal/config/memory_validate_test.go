package config

import (
	"strings"
	"testing"
)

func memValidCfg() *Config {
	return &Config{
		Version: "1.0",
		Agents:  []AgentConfig{{ID: "dev", Model: "claude-sonnet-4-6"}},
		Workflows: []WorkflowConfig{{
			ID:    "wf",
			Steps: []StepConfig{{ID: "run", Agent: "dev"}},
		}},
	}
}

func errorsContain(errs []error, want string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return true
		}
	}
	return false
}

func TestValidate_MemorySettings(t *testing.T) {
	cfg := memValidCfg()
	cfg.Settings.Memory = MemorySettings{Enabled: true, TaskRetention: "720h", MaxInjectChars: 4000}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("valid memory settings rejected: %v", errs)
	}

	cfg = memValidCfg()
	cfg.Settings.Memory.TaskRetention = "not-a-duration"
	if errs := cfg.Validate(); !errorsContain(errs, "task_retention") {
		t.Fatalf("invalid retention accepted: %v", errs)
	}

	cfg = memValidCfg()
	cfg.Settings.Memory.MaxInjectChars = -1
	if errs := cfg.Validate(); !errorsContain(errs, "max_inject_chars") {
		t.Fatalf("negative max_inject_chars accepted: %v", errs)
	}
}

func TestValidate_StepMemoryEnums(t *testing.T) {
	cfg := memValidCfg()
	cfg.Workflows[0].Steps[0].Memory = &MemoryConfig{Recall: []string{"task", "global"}, Memorize: "auto"}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("valid step memory rejected: %v", errs)
	}

	cfg = memValidCfg()
	cfg.Workflows[0].Steps[0].Memory = &MemoryConfig{Recall: []string{"everything"}}
	if errs := cfg.Validate(); !errorsContain(errs, "memory.recall") {
		t.Fatalf("invalid recall tier accepted: %v", errs)
	}

	cfg = memValidCfg()
	cfg.Workflows[0].Steps[0].Memory = &MemoryConfig{Memorize: "maybe"}
	if errs := cfg.Validate(); !errorsContain(errs, "memory.memorize") {
		t.Fatalf("invalid memorize accepted: %v", errs)
	}
}

func TestStepMemoryDefaults(t *testing.T) {
	s := StepConfig{}
	if !s.MemorizeEnabled() {
		t.Error("memorize must default to enabled")
	}
	tiers := s.MemoryRecallTiers()
	if len(tiers) != 2 || tiers[0] != MemoryTierTask || tiers[1] != MemoryTierGlobal {
		t.Errorf("recall must default to both tiers, got %v", tiers)
	}
	s.Memory = &MemoryConfig{Memorize: MemorizeOff, Recall: []string{"global"}}
	if s.MemorizeEnabled() {
		t.Error("memorize: off must disable")
	}
	if got := s.MemoryRecallTiers(); len(got) != 1 || got[0] != "global" {
		t.Errorf("recall filter not honored: %v", got)
	}
}
