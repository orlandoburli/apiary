package opencode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
)

func init() {
	runner.Register("opencode", func() runner.Adapter { return &Runner{} })
}

type Runner struct {
	binary     string
	agent      string
	modelFlag  string
	promptFlag string
	turnsFlag  string
	agentFlag  string
	skillFlag  string
	extraArgs  []string
}

func (r *Runner) ID() string { return "opencode" }

func (r *Runner) Configure(config map[string]any) error {
	if v, ok := config["binary"].(string); ok && v != "" {
		r.binary = v
	} else {
		r.binary = "opencode"
	}

	if v, ok := config["agent"].(string); ok {
		r.agent = v
	}

	if v, ok := config["model_flag"].(string); ok {
		r.modelFlag = v
	} else {
		r.modelFlag = "--model"
	}

	if v, ok := config["prompt_flag"].(string); ok {
		r.promptFlag = v
	} else {
		r.promptFlag = "--prompt"
	}

	if v, ok := config["turns_flag"].(string); ok {
		r.turnsFlag = v
	} else {
		r.turnsFlag = "--max-turns"
	}

	if v, ok := config["agent_flag"].(string); ok {
		r.agentFlag = v
	} else {
		r.agentFlag = "--agent"
	}

	if v, ok := config["skill_flag"].(string); ok {
		r.skillFlag = v
	} else {
		r.skillFlag = "--skill"
	}

	if raw, ok := config["extra_args"].([]any); ok {
		for _, a := range raw {
			if s, ok := a.(string); ok {
				r.extraArgs = append(r.extraArgs, s)
			}
		}
	}

	return nil
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()

	prompt := buildPrompt(req)

	argv := []string{}
	argv = append(argv, r.extraArgs...)

	if r.agentFlag != "" && r.agent != "" {
		argv = append(argv, r.agentFlag, r.agent)
	}

	if r.modelFlag != "" && req.Model != "" {
		argv = append(argv, r.modelFlag, req.Model)
	}

	if r.turnsFlag != "" && req.MaxTurns > 0 {
		argv = append(argv, r.turnsFlag, fmt.Sprintf("%d", req.MaxTurns))
	}

	if r.promptFlag != "" {
		argv = append(argv, r.promptFlag, prompt)
	}

	cmd := exec.CommandContext(ctx, r.binary, argv...)
	cmd.Dir = req.WorkingDir

	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var (
		mu     sync.Mutex
		outBuf bytes.Buffer
		errBuf bytes.Buffer
		logs   []model.LogEntry
	)

	emit := func(level, msg string) {
		entry := model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()}
		mu.Lock()
		logs = append(logs, entry)
		mu.Unlock()
		if req.LogSink != nil {
			req.LogSink(entry)
		}
	}

	emit("debug", fmt.Sprintf("$ %s %s", r.binary, strings.Join(argv, " ")))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return model.RunResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return model.RunResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: starting %q: %w", r.binary, err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			outBuf.WriteString(line + "\n")
			mu.Unlock()
			emit("debug", line)
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			errBuf.WriteString(line + "\n")
			mu.Unlock()
			emit("debug", "stderr: "+line)
		}
	}()

	wg.Wait()
	runErr := cmd.Wait()

	output := strings.TrimSpace(outBuf.String())

	result := model.RunResult{
		WorkerID: req.WorkerID,
		Success:  runErr == nil,
		Output:   output,
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
