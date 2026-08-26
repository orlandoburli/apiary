package config

import (
	"strings"
	"testing"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

// captureWarnings collects the warnings emitted while loading a config, via the
// log package's sink.
func captureWarnings(t *testing.T, cfg *Config) []string {
	t.Helper()
	var got []string
	aplog.SetSink(func(level, msg string) {
		if level == "WARN" {
			got = append(got, msg)
		}
	})
	t.Cleanup(func() { aplog.SetSink(nil) })
	warnDeprecatedResultComment(cfg)
	return got
}

// The completion modes post the workflow memory document — an aggregate no
// single agent's output contains, and on_fail covers runs where the agent
// produced nothing at all. Warning there would point operators at a replacement
// that structurally cannot cover the case.
func TestResultCommentCompletionModesAreNotDeprecated(t *testing.T) {
	for _, mode := range []string{ResultCommentOnComplete, ResultCommentOnFail, ResultCommentAlways, ResultCommentOff} {
		cfg := &Config{Workflows: []WorkflowConfig{{ID: "wf", ResultComment: mode}}}
		if w := captureWarnings(t, cfg); len(w) != 0 {
			t.Errorf("result_comment: %s should not warn, got %v", mode, w)
		}
	}
}

// per_step dumps a step's raw stdout; an agent that wants to report can choose
// what to say with APIARY_PUBLISH instead. That one stays deprecated.
func TestResultCommentPerStepWarns(t *testing.T) {
	cfg := &Config{Workflows: []WorkflowConfig{{ID: "release", ResultComment: ResultCommentPerStep}}}
	warnings := captureWarnings(t, cfg)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}
	w := warnings[0]
	for _, want := range []string{"release", "per_step", "APIARY_PUBLISH", "not deprecated"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning should mention %q: %s", want, w)
		}
	}
}

// settings.result_comment is the global on_complete switch, so it must not warn
// either — it selects a completion mode, not per_step.
func TestGlobalResultCommentSettingDoesNotWarn(t *testing.T) {
	cfg := &Config{Settings: Settings{ResultComment: true}}
	if w := captureWarnings(t, cfg); len(w) != 0 {
		t.Errorf("settings.result_comment should not warn, got %v", w)
	}
}
