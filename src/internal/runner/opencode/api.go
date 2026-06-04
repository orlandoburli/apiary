package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
)

func init() {
	runner.Register("opencode-api", func() runner.Runner { return &APIRunner{} })
}

type Subscription string

const (
	SubGo  Subscription = "go"
	SubZen Subscription = "zen"
)

type APIRunner struct {
	subscription Subscription
	apiKey       string
	apiBaseURL   string
}

func (r *APIRunner) ID() string { return "opencode-api" }

func (r *APIRunner) Configure(config map[string]any) error {
	sub, _ := config["subscription"].(string)
	switch sub {
	case "go":
		r.subscription = SubGo
	case "zen":
		r.subscription = SubZen
	default:
		return fmt.Errorf("opencode-api runner: invalid subscription %q (expected go or zen)", sub)
	}
	if v, ok := config["api_key"].(string); ok {
		r.apiKey = v
	}
	if v, ok := config["base_url"].(string); ok {
		r.apiBaseURL = v
	}
	if r.apiKey == "" {
		return fmt.Errorf("opencode-api runner: api_key is required")
	}
	return nil
}

func (r *APIRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	modelID := r.resolveModelID(req.Model)

	messages := []chatMessage{
		{Role: "system", Content: "You are an AI coding agent."},
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

	raw, _ := json.Marshal(body)

	apiURL := r.apiBaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(raw))
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode-api: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("opencode-api: api request: %w", err)
	}
	defer httpResp.Body.Close()

	respRaw, _ := io.ReadAll(httpResp.Body)

	var resp chatResponse
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return model.RunResult{}, fmt.Errorf("opencode-api: parse response: %w", err)
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
		logs = append(logs, model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()})
		if req.LogSink != nil {
			req.LogSink(model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()})
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

func (r *APIRunner) resolveModelID(model string) string {
	if model == "" {
		return "default"
	}
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return model
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
	Error   *chatError   `json:"error,omitempty"`
}

type chatChoice struct {
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
