package config

import (
	"strings"
	"testing"
)

// eventCfg builds a minimal config with one workflow whose trigger is t.
func eventCfg(t TriggerConfig) *Config {
	return &Config{
		Version: "1",
		Sources: []SourceConfig{{ID: "github", Type: "github"}, {ID: "board", Type: "plane"}},
		Agents:  []AgentConfig{{ID: "eng", Model: "m"}},
		Workflows: []WorkflowConfig{{
			ID:      "wf",
			Trigger: &t,
			Steps:   []StepConfig{{ID: "s1", Agent: "eng"}},
		}},
	}
}

func errsContain(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}

func TestValidateTriggerEvents_OnWhitelist(t *testing.T) {
	if errs := eventCfg(TriggerConfig{On: "pr_merged"}).Validate(); !errsContain(errs, "trigger on \"pr_merged\" not supported") {
		t.Errorf("unknown on: value must be rejected, got %v", errs)
	}
	for _, on := range []string{"", "item", "pr_comment", "pr_review_approved", "pr_review_changes_requested"} {
		if errs := eventCfg(TriggerConfig{On: on}).Validate(); errsContain(errs, "trigger on") {
			t.Errorf("on=%q must be accepted, got %v", on, errs)
		}
	}
}

func TestValidateTriggerEvents_EventOnlyFieldsOnItemTrigger(t *testing.T) {
	if errs := eventCfg(TriggerConfig{CommentContains: "@apiary"}).Validate(); !errsContain(errs, "comment_contains requires on") {
		t.Errorf("comment_contains on item trigger must be rejected, got %v", errs)
	}
	if errs := eventCfg(TriggerConfig{Authors: []string{"me"}}).Validate(); !errsContain(errs, "authors/authors_association are only valid") {
		t.Errorf("authors on item trigger must be rejected, got %v", errs)
	}
	if errs := eventCfg(TriggerConfig{MaxDispatches: 3}).Validate(); !errsContain(errs, "max_dispatches is only valid") {
		t.Errorf("max_dispatches on item trigger must be rejected, got %v", errs)
	}
}

func TestValidateTriggerEvents_CommentContainsScoping(t *testing.T) {
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRReviewApproved, CommentContains: "@apiary"}).Validate(); !errsContain(errs, "comment_contains is only valid with on: pr_comment") {
		t.Errorf("comment_contains on a review trigger must be rejected, got %v", errs)
	}
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, CommentContains: "@apiary"}).Validate(); len(errs) != 0 {
		t.Errorf("comment_contains on pr_comment must be accepted, got %v", errs)
	}
}

func TestValidateTriggerEvents_OnceRejectedAndAssociationEnum(t *testing.T) {
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, Once: true}).Validate(); !errsContain(errs, "once is not valid on an event trigger") {
		t.Errorf("once on event trigger must be rejected, got %v", errs)
	}
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, AuthorsAssociation: []string{"FRIEND"}}).Validate(); !errsContain(errs, "authors_association value \"FRIEND\"") {
		t.Errorf("bad authors_association must be rejected, got %v", errs)
	}
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, AuthorsAssociation: []string{"owner", "MEMBER"}}).Validate(); len(errs) != 0 {
		t.Errorf("case-insensitive association values must be accepted, got %v", errs)
	}
}

func TestValidateTriggerEvents_SourceCapability(t *testing.T) {
	prev := SourceSupportsPREvents
	defer func() { SourceSupportsPREvents = prev }()
	SourceSupportsPREvents = func(sourceType string) bool { return sourceType == "github" }

	// Scoped to a capable source: OK.
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, Match: RouteMatch{Source: "github"}}).Validate(); len(errs) != 0 {
		t.Errorf("capable source must pass, got %v", errs)
	}
	// Scoped to an incapable source: rejected.
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment, Match: RouteMatch{Source: "board"}}).Validate(); !errsContain(errs, "does not support it") {
		t.Errorf("incapable source must be rejected, got %v", errs)
	}
	// Unscoped with at least one capable source: OK.
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment}).Validate(); len(errs) != 0 {
		t.Errorf("unscoped trigger with a capable source must pass, got %v", errs)
	}
	// No capable source at all: rejected.
	SourceSupportsPREvents = func(string) bool { return false }
	if errs := eventCfg(TriggerConfig{On: TriggerOnPRComment}).Validate(); !errsContain(errs, "no configured source supports it") {
		t.Errorf("no capable source must be rejected, got %v", errs)
	}
}
