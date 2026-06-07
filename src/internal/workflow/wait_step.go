package workflow

import (
	"context"
	"fmt"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/config"
)

// RunWaitStep performs ONE check of the external system (e.g. CI status) and
// returns either a terminal result (pass/fail) or StepResult{Pending: true} when
// there is no answer yet. It does NOT loop or block: the scheduler parks the
// instance on a Pending result and calls this again on a later poll cycle, so a
// long CI wait survives daemon restarts instead of holding a worker for hours.
//
// deadline is the absolute time the wait gives up (zero = no deadline). Past the
// deadline the step returns a terminal failure with ci_status=timeout.
func (e *Engine) RunWaitStep(
	ctx context.Context,
	step config.StepConfig,
	sourceID, sourceItemID string,
	deadline time.Time,
) (StepResult, error) {
	if step.WaitFor == nil {
		return StepResult{}, fmt.Errorf("wait_for step %q missing wait_for config", step.ID)
	}

	cfg := step.WaitFor
	switch cfg.Kind {
	case "", "ci":
		return e.checkCIWaitStep(ctx, step, sourceID, sourceItemID, deadline)
	default:
		return StepResult{}, fmt.Errorf("wait_for step %q unsupported kind: %q", step.ID, cfg.Kind)
	}
}

// checkCIWaitStep performs a single CI status check. Transient errors and a
// still-running CI both yield Pending (retry next cycle); a passed/failed CI is
// terminal; passing the deadline is a terminal timeout failure.
func (e *Engine) checkCIWaitStep(
	ctx context.Context,
	step config.StepConfig,
	sourceID, sourceItemID string,
	deadline time.Time,
) (StepResult, error) {
	if e.ciChecker == nil {
		return StepResult{
			Success: false,
			Err:     fmt.Errorf("CI status polling not configured"),
		}, nil
	}

	cfg := step.WaitFor

	// Timeout: give up waiting once past the deadline.
	if !deadline.IsZero() && e.now().After(deadline) {
		aplog.Info("wait_for step %q: CI wait timed out after %v", step.ID, cfg.ParsedMaxDuration())
		return StepResult{
			Success: false,
			StructuredOutput: map[string]any{
				"ci_status": "timeout",
				"reason":    fmt.Sprintf("CI did not complete within %v", cfg.ParsedMaxDuration()),
			},
		}, nil
	}

	status, err := e.ciChecker(ctx, sourceID, sourceItemID)
	if err != nil {
		// Surface at WARN (not DEBUG): a persistent error here — e.g. a token
		// missing 'Pull requests: Read' (403) — would otherwise masquerade as an
		// endless "pending" with no visible cause. Still retried next cycle so it
		// self-heals once the underlying problem (token, transient outage) is fixed.
		aplog.Warn("wait_for step %q: CI check failed (will retry next cycle): %v", step.ID, err)
		return StepResult{Pending: true}, nil
	}

	switch status.Status {
	case "passed":
		return StepResult{
			Success: true,
			StructuredOutput: map[string]any{
				"ci_status": "passed",
				"url":       status.URL,
			},
		}, nil

	case "failed":
		result := StepResult{
			StructuredOutput: map[string]any{
				"ci_status": "failed",
				"url":       status.URL,
				"checks":    checksToMap(status.Checks),
			},
		}
		if cfg.ShouldFailIfNotPassed() {
			result.Success = false
			result.Err = fmt.Errorf("CI checks failed")
		} else {
			result.Success = true
		}
		return result, nil

	case "pending":
		aplog.Debug("wait_for step %q: CI still pending", step.ID)
		return StepResult{Pending: true}, nil

	default:
		// Unknown/empty status: keep waiting rather than failing spuriously. The
		// deadline guarantees this cannot loop forever.
		aplog.Debug("wait_for step %q: CI status %q not yet conclusive", step.ID, status.Status)
		return StepResult{Pending: true}, nil
	}
}

func checksToMap(checks []struct {
	Name   string
	Status string
}) map[string]string {
	m := make(map[string]string)
	for _, c := range checks {
		m[c.Name] = c.Status
	}
	return m
}
