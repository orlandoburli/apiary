// Package script provides a runner adapter that executes an arbitrary shell
// command, injecting Cell fields as environment variables.
package script

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
)

func init() {
	runner.Register("script", func() runner.Runner { return &Runner{} })
}

// Runner executes a shell command with Cell data injected as APIARY_CELL_*
// environment variables. Useful for custom integrations and wrapper scripts.
type Runner struct {
	command string
	shell   string
}

func (r *Runner) ID() string { return "script" }

func (r *Runner) Configure(config map[string]any) error {
	cmd, ok := config["command"].(string)
	if !ok || cmd == "" {
		return fmt.Errorf("script runner: config.command is required")
	}
	r.command = cmd

	if sh, ok := config["shell"].(string); ok && sh != "" {
		r.shell = sh
	} else {
		r.shell = "/bin/sh"
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()

	cmd := exec.CommandContext(ctx, r.shell, "-c", r.command)
	cmd.Dir = req.WorkingDir
	cmd.Env = buildEnv(req)

	var errBuf bytes.Buffer
	var logs []model.LogEntry
	var outBuf bytes.Buffer

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return model.RunResult{}, err
	}
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return model.RunResult{}, fmt.Errorf("script runner: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()
		outBuf.WriteString(line + "\n")
		logs = append(logs, model.LogEntry{
			Level:     "info",
			Message:   line,
			Timestamp: time.Now(),
		})
	}

	runErr := cmd.Wait()

	result := model.RunResult{
		WorkerID: req.WorkerID,
		Success:  runErr == nil,
		Output:   strings.TrimSpace(outBuf.String()),
		Logs:     logs,
		Duration: time.Since(start),
	}

	if runErr != nil {
		stderr := strings.TrimSpace(errBuf.String())
		if stderr != "" {
			result.Error = fmt.Errorf("%w\nstderr: %s", runErr, stderr)
		} else {
			result.Error = runErr
		}
	}

	return result, nil
}

// buildEnv assembles the subprocess environment with APIARY_CELL_* variables.
func buildEnv(req model.RunRequest) []string {
	env := os.Environ()

	// Cell fields
	env = append(env,
		"APIARY_CELL_ID="+req.Cell.ID,
		"APIARY_CELL_SOURCE_ID="+req.Cell.SourceID,
		"APIARY_CELL_TITLE="+req.Cell.Title,
		"APIARY_CELL_DESCRIPTION="+req.Cell.Description,
		"APIARY_CELL_TYPE="+req.Cell.Type,
		"APIARY_CELL_PRIORITY="+req.Cell.Priority,
		"APIARY_CELL_URL="+req.Cell.URL,
		"APIARY_CELL_LABELS="+strings.Join(req.Cell.Labels, ","),
	)

	// Run context
	env = append(env,
		"APIARY_WORKER_ID="+req.WorkerID,
		"APIARY_MODEL="+req.Model,
		"APIARY_WORKING_DIR="+req.WorkingDir,
	)

	// User-defined extra env
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}

	return env
}
