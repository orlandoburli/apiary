// Package execution provides generic process-execution engines used by
// provider-specific runners (claude, opencode, etc.). These engines handle
// subprocess management (cli) and HTTP API calls (api).
package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// CliRunner manages a CLI subprocess with stdout/stderr streaming, PID tracking,
// and heartbeats. Used by claude, opencode-cli, and similar providers.
type CliRunner struct {
	command    string
	args       []string
	modelFlag  string
	promptFlag string
	turnsFlag  string
}

func (r *CliRunner) ID() string { return "cli" }

func (r *CliRunner) Configure(config map[string]any) error {
	if cmd, ok := config["command"].(string); ok && cmd != "" {
		r.command = cmd
	} else if r.command == "" {
		return fmt.Errorf("cli runner: config.command is required")
	}
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

func (r *CliRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	argv := append([]string{}, r.args...)
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

	var (
		mu          sync.Mutex
		outBuf      bytes.Buffer
		errBuf      bytes.Buffer
		logs        []model.LogEntry
		finalResult string
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

	emit("debug", fmt.Sprintf("$ %s %s", r.command, strings.Join(argv, " ")))
	emit("debug", "prompt sent to agent:\n"+prompt)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return model.RunResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return model.RunResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return model.RunResult{}, fmt.Errorf("cli runner: starting %q: %w", r.command, err)
	}

	// PID tracking
	if req.SetPID != nil {
		req.SetPID(cmd.Process.Pid)
	}
	heartbeatDone := make(chan struct{})
	if req.Heartbeat != nil {
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					req.Heartbeat()
				case <-heartbeatDone:
					return
				}
			}
		}()
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
			if res, ok := finalResultText(line); ok {
				mu.Lock()
				finalResult = res
				mu.Unlock()
			}
			if pretty, ok := formatStreamLine(line); ok {
				emit("debug", pretty)
			} else {
				emit("debug", line)
			}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
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
	close(heartbeatDone)

	output := strings.TrimSpace(outBuf.String())
	if strings.TrimSpace(finalResult) != "" {
		output = strings.TrimSpace(finalResult)
	}

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

// stream-json event types (Claude output format)
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Result  string `json:"result"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
	DurationMs   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError      bool    `json:"is_error"`
}

func formatStreamLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil || ev.Type == "" {
		return "", false
	}
	switch ev.Type {
	case "system":
		label := ev.Subtype
		if label == "" {
			label = "system"
		}
		if ev.Model != "" {
			return fmt.Sprintf("[system:%s] model=%s", label, ev.Model), true
		}
		return fmt.Sprintf("[system:%s]", label), true
	case "assistant":
		var parts []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					parts = append(parts, "[assistant] "+t)
				}
			case "tool_use":
				parts = append(parts, fmt.Sprintf("[tool→ %s] %s", c.Name, truncateInput(c.Input)))
			}
		}
		if len(parts) == 0 {
			return "[assistant]", true
		}
		return strings.Join(parts, "\n"), true
	case "user":
		var parts []string
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				parts = append(parts, "[tool← result] "+truncateInput(c.Content))
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, "\n"), true
	case "result":
		status := ev.Subtype
		if status == "" {
			if ev.IsError {
				status = "error"
			} else {
				status = "success"
			}
		}
		out := fmt.Sprintf("[result:%s] turns=%d duration=%s", status, ev.NumTurns,
			(time.Duration(ev.DurationMs)*time.Millisecond).Round(time.Millisecond))
		if ev.TotalCostUSD > 0 {
			out += fmt.Sprintf(" cost=$%.4f", ev.TotalCostUSD)
		}
		if r := strings.TrimSpace(ev.Result); r != "" {
			out += "\n" + r
		}
		return out, true
	}
	return "", false
}

func finalResultText(line string) (string, bool) {
	if !strings.Contains(line, `"type":"result"`) {
		return "", false
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil || ev.Type != "result" {
		return "", false
	}
	if strings.TrimSpace(ev.Result) == "" {
		return "", false
	}
	return ev.Result, true
}

func truncateInput(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	const max = 240
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func buildPrompt(req model.RunRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n", req.Cell.Title)
	if req.Cell.Type != "" {
		fmt.Fprintf(&b, "Type: %s\n", req.Cell.Type)
	}
	if req.Cell.Priority != "" {
		fmt.Fprintf(&b, "Priority: %s\n", req.Cell.Priority)
	}
	if len(req.Cell.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(req.Cell.Labels, ", "))
	}
	if req.Cell.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", req.Cell.URL)
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
