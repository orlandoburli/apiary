package improve

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	runnerimpl "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/execution"
)

func init() {
	runnerimpl.Register("improvetest-cli", func() runnerimpl.Runner { return &execution.CliRunner{} })
}

// unstartableAdvisor resolves to a real CLI runner whose binary does not exist,
// so Run fails at exec the way an oversized argv does (#493).
func unstartableAdvisor() *Advisor {
	return &Advisor{
		RunnerID: "claude",
		Model:    "test-model",
		Runner: &config.RunnerConfig{
			ID:       "claude",
			Type:     "cli",
			Provider: "improvetest",
			Config:   map[string]any{"command": "/nonexistent/apiary-improve-test"},
		},
	}
}

func assertExecErrorSurvives(t *testing.T, out *RunOutcome, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a runner that cannot start must fail the run")
	}
	msg := err.Error()
	if strings.Contains(msg, "<nil>") {
		t.Errorf("the exec failure was lost: %q", msg)
	}
	if !strings.Contains(msg, "apiary-improve-test") {
		t.Errorf("the error should name the command that failed to start, got %q", msg)
	}
	if len(out.Attempts) != 1 || out.Attempts[0].Err == "" {
		t.Errorf("the attempt should record the error, got %+v", out.Attempts)
	}
}

func TestRunAdvisorReportsAFailedExec(t *testing.T) {
	out, err := RunAdvisor(context.Background(), &config.Config{}, unstartableAdvisor(), "prompt", Knobs{}, t.TempDir())
	assertExecErrorSurvives(t, out, err)
}

func TestRunStructuredReportsAFailedExec(t *testing.T) {
	out, err := runStructured(context.Background(), &config.Config{}, unstartableAdvisor(), "prompt", Knobs{}, t.TempDir())
	assertExecErrorSurvives(t, out, err)
}

// Failure detection can mark a run failed while res.Error stays nil.
func TestRunFailureNeverPrintsNil(t *testing.T) {
	for _, res := range []model.RunResult{
		{FailureKind: model.FailureAborted},
		{},
	} {
		if msg := runFailure("claude", &res).Error(); strings.Contains(msg, "<nil>") {
			t.Errorf("runFailure(%+v) = %q", res, msg)
		}
	}
}
