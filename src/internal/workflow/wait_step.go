package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/source"
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
	instID string,
	step config.StepConfig,
	sourceID, sourceItemID string,
	deadline time.Time,
) (StepResult, error) {
	if step.WaitFor == nil {
		return StepResult{}, fmt.Errorf("wait_for step %q missing wait_for config", step.ID)
	}

	cfg := step.WaitFor
	switch cfg.Kind {
	case "", config.WaitKindCI:
		return e.checkCIWaitStep(ctx, instID, step, sourceID, sourceItemID, deadline)
	case config.WaitKindDependency:
		return e.checkDependencyWaitStep(ctx, instID, step, sourceID, sourceItemID, deadline)
	default:
		return StepResult{}, fmt.Errorf("wait_for step %q unsupported kind: %q", step.ID, cfg.Kind)
	}
}

// checkCIWaitStep performs a single CI status check. Transient errors and a
// still-running CI both yield Pending (retry next cycle); a passed/failed CI is
// terminal; passing the deadline is a terminal timeout failure.
func (e *Engine) checkCIWaitStep(
	ctx context.Context,
	instID string,
	step config.StepConfig,
	sourceID, sourceItemID string,
	deadline time.Time,
) (StepResult, error) {
	if e.ciChecker == nil {
		return e.unsupportedWait(ctx, instID, step, "ci",
			fmt.Errorf("CI status polling not configured")), nil
	}

	cfg := step.WaitFor

	// Timeout: once past the deadline, fail the step — or, with on_timeout: hold,
	// keep the instance parked for a human instead of giving up.
	if !deadline.IsZero() && e.now().After(deadline) {
		if cfg.TimeoutAction() == config.OnTimeoutHold {
			aplog.Info("wait_for step %q: CI wait exceeded %v — holding (on_timeout: hold)", step.ID, cfg.ParsedMaxDuration())
			e.recordCIPoll(ctx, instID, step.ID, "timeout", "",
				fmt.Sprintf("CI did not complete within %v; holding for manual resolution", cfg.ParsedMaxDuration()))
			return StepResult{Pending: true}, nil
		}
		aplog.Info("wait_for step %q: CI wait timed out after %v", step.ID, cfg.ParsedMaxDuration())
		e.recordCIPoll(ctx, instID, step.ID, "timeout", "",
			fmt.Sprintf("CI did not complete within %v", cfg.ParsedMaxDuration()))
		return StepResult{
			Success: false,
			StructuredOutput: map[string]any{
				"ci_status": "timeout",
				"reason":    fmt.Sprintf("CI did not complete within %v", cfg.ParsedMaxDuration()),
			},
		}, nil
	}

	status, err := e.ciChecker(ctx, sourceID, sourceItemID)
	if errors.Is(err, source.ErrUnsupported) {
		// The source will never gain the capability by waiting: fail now with the
		// cause named, rather than polling until max_duration with a WARN a
		// cycle. `apiary validate` normally catches this at config load; reaching
		// here means the config was never linted (or the source changed under a
		// running daemon), so the message has to stand on its own (#425).
		return e.unsupportedWait(ctx, instID, step, "ci", err), nil
	}
	if err != nil {
		// Surface at WARN (not DEBUG): a persistent error here — e.g. a token
		// missing 'Pull requests: Read' (403) — would otherwise masquerade as an
		// endless "pending" with no visible cause. Still retried next cycle so it
		// self-heals once the underlying problem (token, transient outage) is fixed.
		aplog.Warn("wait_for step %q: CI check failed (will retry next cycle): %v", step.ID, err)
		e.recordCIPoll(ctx, instID, step.ID, "error", "", err.Error())
		return StepResult{Pending: true}, nil
	}

	// Record every poll result (passed/failed/pending/unknown) with the per-check
	// detail, so the wait's full history is auditable from the dashboard.
	e.recordCIPoll(ctx, instID, step.ID, normalizeCIStatus(status.Status), status.URL, encodeChecks(status.Checks))

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

	case "conflict":
		// A merge conflict cannot be resolved by waiting — stop polling at once and
		// fail the step (regardless of fail_if_not_passed) so the conflict is handed
		// straight back to the engineer agent to rebase/resolve.
		aplog.Info("wait_for step %q: PR has merge conflicts — aborting CI wait", step.ID)
		return StepResult{
			Success:  false,
			Conflict: true,
			Err:      fmt.Errorf("PR has merge conflicts"),
			StructuredOutput: map[string]any{
				"ci_status": "conflict",
				"url":       status.URL,
			},
		}, nil

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

// unsupportedWait fails a wait_for step whose backing capability is missing.
// The failure is logged and recorded as a poll with status "unsupported", so
// the cause is visible in the daemon log AND in the wait's history — a wait
// that dies in milliseconds with nothing written anywhere is the worst
// possible diagnostic, and it is exactly what a wait_for/ci step used to do on
// a source that cannot poll CI (#425).
func (e *Engine) unsupportedWait(ctx context.Context, instID string, step config.StepConfig, kind string, cause error) StepResult {
	aplog.Error("wait_for step %q (kind: %s): %v — failing the step", step.ID, kind, cause)
	e.recordCIPoll(ctx, instID, step.ID, "unsupported", "", cause.Error())
	return StepResult{
		Success: false,
		Err:     cause,
		StructuredOutput: map[string]any{
			"ci_status": "unsupported",
			"reason":    cause.Error(),
		},
	}
}

// checkDependencyWaitStep performs a single blocker check for a wait_for step
// with kind "dependency". Transient lookup errors and any still-unsatisfied
// blocker both yield Pending (the instance stays parked and is re-checked next
// poll cycle); all blockers satisfied — merged and/or Done, per satisfied_when —
// is terminal success, auto-resuming the workflow. Past the deadline the step
// honours on_timeout: hold keeps parking for a human (the default for this
// kind), fail fails the step. Every check is recorded like a CI poll, so the
// wait's history is auditable from the dashboard.
func (e *Engine) checkDependencyWaitStep(
	ctx context.Context,
	instID string,
	step config.StepConfig,
	sourceID, sourceItemID string,
	deadline time.Time,
) (StepResult, error) {
	if e.depChecker == nil {
		return e.unsupportedWait(ctx, instID, step, "dependency",
			fmt.Errorf("dependency (blocker) polling not configured")), nil
	}

	cfg := step.WaitFor

	// Timeout: hold keeps the instance parked for a human; fail gives up.
	if !deadline.IsZero() && e.now().After(deadline) {
		if cfg.TimeoutAction() == config.OnTimeoutFail {
			aplog.Info("wait_for step %q: dependency wait timed out after %v", step.ID, cfg.ParsedMaxDuration())
			e.recordCIPoll(ctx, instID, step.ID, "timeout", "",
				fmt.Sprintf("blockers not satisfied within %v", cfg.ParsedMaxDuration()))
			return StepResult{
				Success: false,
				StructuredOutput: map[string]any{
					"dependency_status": "timeout",
					"reason":            fmt.Sprintf("blockers not satisfied within %v", cfg.ParsedMaxDuration()),
				},
			}, nil
		}
		aplog.Info("wait_for step %q: dependency wait exceeded %v — holding (on_timeout: hold)", step.ID, cfg.ParsedMaxDuration())
		e.recordCIPoll(ctx, instID, step.ID, "timeout", "",
			fmt.Sprintf("blockers not satisfied within %v; holding for manual resolution", cfg.ParsedMaxDuration()))
		return StepResult{Pending: true}, nil
	}

	blockers, err := e.depChecker(ctx, sourceID, sourceItemID, cfg.BlockerLinkType)
	if errors.Is(err, source.ErrUnsupported) {
		return e.unsupportedWait(ctx, instID, step, "dependency", err), nil
	}
	if err != nil {
		// WARN, not DEBUG: a persistent error (missing scope, wrong link type)
		// would otherwise masquerade as an endless wait with no visible cause.
		// Retried next cycle so it self-heals once the underlying problem is fixed.
		aplog.Warn("wait_for step %q: blocker check failed (will retry next cycle): %v", step.ID, err)
		e.recordCIPoll(ctx, instID, step.ID, "error", "", err.Error())
		return StepResult{Pending: true}, nil
	}

	conditions := cfg.EffectiveSatisfiedWhen()
	unsatisfied := make([]source.BlockerRef, 0, len(blockers))
	for _, b := range blockers {
		if !blockerSatisfied(b, conditions) {
			unsatisfied = append(unsatisfied, b)
		}
	}

	if len(unsatisfied) > 0 {
		aplog.Debug("wait_for step %q: %d of %d blocker(s) still unsatisfied", step.ID, len(unsatisfied), len(blockers))
		e.recordCIPoll(ctx, instID, step.ID, "pending", "", encodeBlockers(unsatisfied))
		return StepResult{Pending: true}, nil
	}

	e.recordCIPoll(ctx, instID, step.ID, "passed", "", encodeBlockers(blockers))
	return StepResult{
		Success: true,
		StructuredOutput: map[string]any{
			"dependency_status": "satisfied",
			"blockers":          blockersToMap(blockers),
		},
	}, nil
}

// blockerSatisfied reports whether one blocker meets ANY of the satisfied_when
// conditions: "merged" — a PR linked to the blocker is merged; "done" — the
// blocker's status is Done-category (resolved/closed).
func blockerSatisfied(b source.BlockerRef, conditions []string) bool {
	for _, cond := range conditions {
		switch cond {
		case config.BlockerSatisfiedMerged:
			if b.Merged {
				return true
			}
		case config.BlockerSatisfiedDone:
			if b.State == "done" {
				return true
			}
		}
	}
	return false
}

// blockersToMap renders blockers as reference→state for structured step output.
// A merged blocker reports "merged" so the output shows which condition held.
func blockersToMap(blockers []source.BlockerRef) map[string]string {
	m := make(map[string]string, len(blockers))
	for _, b := range blockers {
		state := b.State
		if b.Merged {
			state = "merged"
		}
		m[b.Number] = state
	}
	return m
}

// encodeBlockers renders blockers as compact JSON for the recorded poll's
// detail column. Returns "" when there are none.
func encodeBlockers(blockers []source.BlockerRef) string {
	if len(blockers) == 0 {
		return ""
	}
	b, err := json.Marshal(blockersToMap(blockers))
	if err != nil {
		return ""
	}
	return string(b)
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

// normalizeCIStatus maps an empty/unrecognized CI status to "unknown" so a
// recorded poll always carries a meaningful, non-empty status.
func normalizeCIStatus(s string) string {
	switch s {
	case "passed", "failed", "pending", "conflict":
		return s
	default:
		return "unknown"
	}
}

// encodeChecks renders the per-check name→status map as compact JSON for the
// recorded poll's detail column. Returns "" when there are no checks.
func encodeChecks(checks []struct {
	Name   string
	Status string
}) string {
	if len(checks) == 0 {
		return ""
	}
	b, err := json.Marshal(checksToMap(checks))
	if err != nil {
		return ""
	}
	return string(b)
}
