// Package cli provides a runner adapter that invokes any agent CLI tool
// (e.g. opencode, gemini) as a subprocess. The tool manages its own
// authentication — Apiary never handles or stores credentials.
package cli

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
	runner.Register("cli", func() runner.Adapter { return &Runner{} })
}

// Runner invokes an agent CLI tool as a subprocess, passing the task
// prompt via stdin (default) or a configurable flag.
type Runner struct {
	command    string
	args       []string
	modelFlag  string
	promptFlag string
	turnsFlag  string
}

func (r *Runner) ID() string { return "cli" }

func (r *Runner) Configure(config map[string]any) error {
	cmd, ok := config["command"].(string)
	if !ok || cmd == "" {
		return fmt.Errorf("cli runner: config.command is required")
	}
	r.command = cmd

	if v, ok := config["model_flag"].(string); ok {
		r.modelFlag = v
	}
	if v, ok := config["prompt_flag"].(string); ok {
		r.promptFlag = v
	}
	if v, ok := config["turns_flag"].(string); ok {
		r.turnsFlag = v
	}
	if raw, ok := config["args"].([]any); ok {
		for _, a := range raw {
			if s, ok := a.(string); ok {
				r.args = append(r.args, s)
			}
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()

	prompt := buildPrompt(req)

	argv := []string{}
	argv = append(argv, r.args...)

	if r.modelFlag != "" && req.Model != "" {
		argv = append(argv, r.modelFlag, req.Model)
	}
	if r.turnsFlag != "" && req.MaxTurns > 0 {
		argv = append(argv, r.turnsFlag, fmt.Sprintf("%d", req.MaxTurns))
	}
	if r.promptFlag != "" {
		argv = append(argv, r.promptFlag, prompt)
	}

	cmd := exec.CommandContext(ctx, r.command, argv...)
	cmd.Dir = req.WorkingDir

	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if r.promptFlag == "" {
		cmd.Stdin = strings.NewReader(prompt)
	}

	var outBuf, errBuf bytes.Buffer
	var logs []model.LogEntry

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return model.RunResult{}, err
	}
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return model.RunResult{}, fmt.Errorf("cli runner: starting %q: %w", r.command, err)
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

// buildPrompt formats a Cell into a plain-text prompt for the agent CLI.
func buildPrompt(req model.RunRequest) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Task: %s\n", req.Cell.Title))

	if req.Cell.Type != "" {
		b.WriteString(fmt.Sprintf("Type: %s\n", req.Cell.Type))
	}
	if req.Cell.Priority != "" {
		b.WriteString(fmt.Sprintf("Priority: %s\n", req.Cell.Priority))
	}
	if len(req.Cell.Labels) > 0 {
		b.WriteString(fmt.Sprintf("Labels: %s\n", strings.Join(req.Cell.Labels, ", ")))
	}
	if req.Cell.URL != "" {
		b.WriteString(fmt.Sprintf("URL: %s\n", req.Cell.URL))
	}

	if req.Cell.Description != "" {
		b.WriteString("\n")
		b.WriteString(req.Cell.Description)
		b.WriteString("\n")
	}

	if req.SystemAppend != "" {
		b.WriteString("\n")
		b.WriteString(req.SystemAppend)
		b.WriteString("\n")
	}

	return b.String()
}
