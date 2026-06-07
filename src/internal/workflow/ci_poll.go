package workflow

import (
	"context"
	"fmt"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/config"
)

// RunPollStep executes a poll step, actively querying the external system
// (e.g., CI status) at regular intervals until success, failure, or timeout.
func (e *Engine) RunPollStep(
	ctx context.Context,
	step config.StepConfig,
	sourceID, sourceItemID string,
) (StepResult, error) {
	if step.PollConfig == nil {
		return StepResult{}, fmt.Errorf("poll step %q missing poll config", step.ID)
	}

	cfg := step.PollConfig
	switch cfg.Kind {
	case "", "ci":
		return e.runCIPollStep(ctx, step, sourceID, sourceItemID)
	default:
		return StepResult{}, fmt.Errorf("poll step %q unsupported kind: %q", step.ID, cfg.Kind)
	}
}

// runCIPollStep polls the CI status of a PR/branch until it passes, fails, or times out.
func (e *Engine) runCIPollStep(
	ctx context.Context,
	step config.StepConfig,
	sourceID, sourceItemID string,
) (StepResult, error) {
	if e.ciChecker == nil {
		return StepResult{
			Success: false,
			Err:     fmt.Errorf("CI status polling not configured"),
		}, nil
	}

	cfg := step.PollConfig

	checkInterval := cfg.ParsedCheckInterval()
	maxDuration := cfg.ParsedMaxDuration()
	deadline := time.Now().Add(maxDuration)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return StepResult{
				Success: false,
				Err:     ctx.Err(),
			}, nil

		case <-time.After(time.Until(deadline)):
			// Timeout reached.
			return StepResult{
				Success: false,
				StructuredOutput: map[string]any{
					"ci_status": "timeout",
					"reason":    fmt.Sprintf("CI did not complete within %v", maxDuration),
				},
			}, nil

		case <-ticker.C:
			// Query current CI status.
			status, err := e.ciChecker(ctx, sourceID, sourceItemID)
			if err != nil {
				// Transient error; retry.
				aplog.Debug("poll step %q: CI check failed (retrying): %v", step.ID, err)
				continue
			}

			switch status.Status {
			case "passed":
				// CI is green; step succeeds.
				return StepResult{
					Success: true,
					StructuredOutput: map[string]any{
						"ci_status": "passed",
						"url":       status.URL,
					},
				}, nil

			case "failed":
				// CI is red; step fails (or continues based on config).
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
				// Still running; keep polling.
				aplog.Debug("poll step %q: CI still pending, retrying in %v", step.ID, checkInterval)
				continue

			default:
				// Unknown status.
				return StepResult{
					Success: false,
					StructuredOutput: map[string]any{
						"ci_status": status.Status,
						"reason":    "Unknown CI status",
					},
				}, nil
			}
		}
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
