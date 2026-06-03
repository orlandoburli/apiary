package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type Mode string

const (
	ModeCLI Mode = "cli"
	ModeAPI Mode = "api"
)

type Subscription string

const (
	SubGo  Subscription = "go"
	SubZen Subscription = "zen"
)

type Runner struct {
	mode         Mode
	subscription Subscription

	// CLI mode fields
	binary     string
	agent      string
	modelFlag  string
	promptFlag string
	turnsFlag  string
	agentFlag  string
	skillFlag  string
	extraArgs  []string

	// API mode fields
	apiKey     string
	apiBaseURL string
}

func (r *Runner) ID() string { return "opencode" }

func (r *Runner) Configure(config map[string]any) error {
	mode, _ := config["mode"].(string)
	switch mode {
	case "api":
		r.mode = ModeAPI
	case "", "cli":
		r.mode = ModeCLI
	default:
		return fmt.Errorf("opencode runner: invalid mode %q (expected cli or api)", mode)
	}

	sub, _ := config["subscription"].(string)
	switch sub {
	case "go":
		r.subscription = SubGo
	case "zen":
		r.subscription = SubZen
	case "", "cli":
	default:
		return fmt.Errorf("opencode runner: invalid subscription %q (expected go or zen)", sub)
	}

	if r.mode == ModeCLI {
		return r.configureCLI(config)
	}
	return r.configureAPI(config)
}

func (r *Runner) configureCLI(config map[string]any) error {
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

func (r *Runner) configureAPI(config map[string]any) error {
	key, ok := config["api_key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("opencode runner: config.api_key is required in api mode")
	}
	r.apiKey = key

	switch r.subscription {
	case SubGo:
		r.apiBaseURL = "https://opencode.ai/zen/go/v1"
	case SubZen:
		r.apiBaseURL = "https://opencode.ai/zen/v1"
	default:
		return fmt.Errorf("opencode runner: subscription %q requires api_key", r.subscription)
	}

	if v, ok := config["base_url"].(string); ok && v != "" {
		r.apiBaseURL = v
	}

	return nil
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	if r.mode == ModeCLI {
		return r.runCLI(ctx, req)
	}
	return r.runAPI(ctx, req)
}

func (r *Runner) runCLI(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
	Error   *chatError   `json:"error,omitempty"`
}

type chatChoice struct {
	Index   int         `json:"index"`
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (r *Runner) runAPI(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	modelID := r.resolveModelID(req.Model)

	messages := []chatMessage{
		{Role: "system", Content: "You are an AI coding agent. Complete the following task based on the instructions provided."},
		{Role: "user", Content: prompt},
	}

	if req.SystemAppend != "" {
		messages[0].Content += "\n\n" + req.SystemAppend
	}

	body := chatRequest{
		Model:     modelID,
		Messages:  messages,
		MaxTokens: req.MaxTurns * 4096,
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: marshal request: %w", err)
	}

	apiURL := r.apiBaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: api request: %w", err)
	}
	defer httpResp.Body.Close()

	respRaw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: read response: %w", err)
	}

	var resp chatResponse
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return model.RunResult{}, fmt.Errorf("opencode runner: parse response: %w", err)
	}

	if resp.Error != nil {
		return model.RunResult{
			WorkerID: req.WorkerID,
			Success:  false,
			Duration: time.Since(start),
			Error:    fmt.Errorf("opencode api: %s (%s)", resp.Error.Message, resp.Error.Type),
		}, nil
	}

	var logs []model.LogEntry
	emit := func(level, msg string) {
		entry := model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()}
		logs = append(logs, entry)
		if req.LogSink != nil {
			req.LogSink(entry)
		}
	}

	emit("debug", fmt.Sprintf("POST %s model=%s", apiURL, modelID))

	var output string
	for _, choice := range resp.Choices {
		if choice.Message.Content != "" {
			output += choice.Message.Content
			emit("assistant", choice.Message.Content)
		}
	}

	if resp.Usage != nil {
		emit("debug", fmt.Sprintf("tokens: %d prompt + %d completion = %d total",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens))
	}

	result := model.RunResult{
		WorkerID: req.WorkerID,
		Success:  true,
		Output:   strings.TrimSpace(output),
		Logs:     logs,
		Duration: time.Since(start),
	}

	if httpResp.StatusCode >= 400 {
		result.Success = false
		result.Error = fmt.Errorf("opencode api: HTTP %d", httpResp.StatusCode)
	}

	return result, nil
}

func (r *Runner) resolveModelID(model string) string {
	if model == "" {
		return "default"
	}

	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}

	return model
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
