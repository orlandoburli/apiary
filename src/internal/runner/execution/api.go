package execution

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
)

// ApiRunner invokes an LLM API via HTTP POST. Provider-specific details like
// URL path, auth header, and request/response schemas are configured at creation
// time by the provider factory.
type ApiRunner struct {
	// Endpoint is the full URL (e.g. "https://api.opencode.ai/v1/chat/completions").
	Endpoint string

	// AuthHeader is the Authorization header value (e.g. "Bearer sk-...").
	AuthHeader string

	// BuildBody builds the JSON request body from the task prompt and model.
	// If nil, the default builds a simple chat-completion request.
	BuildBody func(prompt, model string, maxTokens int) ([]byte, error)

	// ParseResponse extracts the output text from the HTTP response body.
	// Returns the output text and an optional error message.
	// If nil, the default reads the "choices[0].message.content" field.
	ParseResponse func(body []byte) (output, errMsg string, isError bool)
}

func (r *ApiRunner) ID() string { return "api" }

func (r *ApiRunner) Configure(config map[string]any) error {
	if v, ok := config["endpoint"].(string); ok && v != "" {
		r.Endpoint = v
	}
	if v, ok := config["api_key"].(string); ok && v != "" {
		r.AuthHeader = "Bearer " + v
	}
	if v, ok := config["base_url"].(string); ok && v != "" && r.Endpoint == "" {
		r.Endpoint = v + "/chat/completions"
	}
	return nil
}

func (r *ApiRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	buildBody := r.BuildBody
	if buildBody == nil {
		buildBody = defaultBuildBody
	}

	parseResp := r.ParseResponse
	if parseResp == nil {
		parseResp = defaultParseResponse
	}

	// MaxTurns sizes the response token budget here: a turn cap has no direct
	// equivalent for a single-shot HTTP call. 0 means uncapped upstream, so
	// fall back to the budget the old hardcoded 15-turn default produced.
	maxTokens := req.MaxTurns * 4096
	if maxTokens <= 0 {
		maxTokens = 15 * 4096
	}
	raw, err := buildBody(prompt, req.Model, maxTokens)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("api runner: build body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return model.RunResult{}, fmt.Errorf("api runner: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.AuthHeader != "" {
		httpReq.Header.Set("Authorization", r.AuthHeader)
	}

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return model.RunResult{}, fmt.Errorf("api runner: request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respRaw, _ := io.ReadAll(httpResp.Body)

	var logs []model.LogEntry
	emit := func(level, msg string) {
		logs = append(logs, model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()})
		if req.LogSink != nil {
			req.LogSink(model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()})
		}
	}

	emit("debug", fmt.Sprintf("POST %s", r.Endpoint))

	output, errMsg, isError := parseResp(respRaw)
	if isError {
		return model.RunResult{
			WorkerID: req.WorkerID,
			Success:  false,
			Output:   errMsg,
			Logs:     logs,
			Duration: time.Since(start),
			Error:    fmt.Errorf("api error: %s", errMsg),
		}, nil
	}

	if httpResp.StatusCode >= 400 {
		return model.RunResult{
			WorkerID: req.WorkerID,
			Success:  false,
			Output:   output,
			Logs:     logs,
			Duration: time.Since(start),
			Error:    fmt.Errorf("api: HTTP %d", httpResp.StatusCode),
		}, nil
	}

	emit("debug", output)
	usage := parseUsage(respRaw)
	result := model.RunResult{
		WorkerID: req.WorkerID,
		Success:  true,
		Output:   strings.TrimSpace(output),
		Logs:     logs,
		Duration: time.Since(start),
		Usage:    usage,
	}
	// Extract APIARY_OUTPUT / APIARY_SUMMARY sentinels into structured fields.
	applyStructured(&result)
	return result, nil
}

func defaultBuildBody(prompt, model string, maxTokens int) ([]byte, error) {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are an AI coding agent."},
			{"role": "user", "content": prompt},
		},
		"max_tokens": maxTokens,
	}
	return json.Marshal(body)
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// anthropicResponse supports the Anthropic Messages API response format.
type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func parseUsage(body []byte) *model.Usage {
	// Try OpenAI-compatible format first
	var openAI chatResponse
	if err := json.Unmarshal(body, &openAI); err == nil && openAI.Usage != nil {
		return &model.Usage{
			InputTokens:  openAI.Usage.PromptTokens,
			OutputTokens: openAI.Usage.CompletionTokens,
			TotalTokens:  openAI.Usage.TotalTokens,
		}
	}
	// Try Anthropic format
	var anthropic anthropicResponse
	if err := json.Unmarshal(body, &anthropic); err == nil && anthropic.Usage != nil {
		return &model.Usage{
			InputTokens:  anthropic.Usage.InputTokens,
			OutputTokens: anthropic.Usage.OutputTokens,
			TotalTokens:  anthropic.Usage.InputTokens + anthropic.Usage.OutputTokens,
		}
	}
	return nil
}

func defaultParseResponse(body []byte) (output, errMsg string, isError bool) {
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Sprintf("parse error: %v", err), true
	}
	if resp.Error != nil {
		return "", fmt.Sprintf("%s (%s)", resp.Error.Message, resp.Error.Type), true
	}
	for _, c := range resp.Choices {
		if c.Message.Content != "" {
			output += c.Message.Content
		}
	}
	return output, "", false
}
