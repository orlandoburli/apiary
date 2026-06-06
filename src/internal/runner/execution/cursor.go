package execution

import (
	"bufio"
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

const cursorDefaultBaseURL = "https://api.cursor.com"

// CursorRunner invokes a Cursor Cloud Agent via the Cursor Agents API.
// It creates a new agent per task, streams the run via SSE, and returns
// the final result text.
type CursorRunner struct {
	BaseURL string // default: https://api.cursor.com
	APIKey  string // Cursor API key (cursor_ prefix)
	RepoURL string // GitHub repository URL for the agent workspace (optional)
}

func (r *CursorRunner) ID() string { return "cursor-api" }

func (r *CursorRunner) Configure(config map[string]any) error {
	if v, ok := config["api_key"].(string); ok && v != "" {
		r.APIKey = v
	}
	if v, ok := config["base_url"].(string); ok && v != "" {
		r.BaseURL = v
	}
	if v, ok := config["repo_url"].(string); ok && v != "" {
		r.RepoURL = v
	}
	if r.APIKey == "" {
		return fmt.Errorf("cursor runner: api_key is required")
	}
	if r.BaseURL == "" {
		r.BaseURL = cursorDefaultBaseURL
	}
	return nil
}

func (r *CursorRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	var logs []model.LogEntry
	emit := func(level, msg string) {
		entry := model.LogEntry{Level: level, Message: msg, Timestamp: time.Now()}
		logs = append(logs, entry)
		if req.LogSink != nil {
			req.LogSink(entry)
		}
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
	defer close(heartbeatDone)

	agentID, runID, err := r.createAgent(ctx, prompt, req.Model)
	if err != nil {
		return model.RunResult{
			WorkerID: req.WorkerID,
			Success:  false,
			Logs:     logs,
			Duration: time.Since(start),
			Error:    err,
		}, nil
	}
	emit("debug", fmt.Sprintf("cursor agent=%s run=%s", agentID, runID))

	output, runErr := r.streamRun(ctx, agentID, runID, emit)
	result := model.RunResult{
		WorkerID: req.WorkerID,
		Success:  runErr == nil,
		Output:   strings.TrimSpace(output),
		Logs:     logs,
		Duration: time.Since(start),
		Error:    runErr,
	}
	applyStructured(&result)
	return result, nil
}

// createAgent posts to /v1/agents and returns the agent ID and initial run ID.
func (r *CursorRunner) createAgent(ctx context.Context, prompt, modelID string) (agentID, runID string, err error) {
	body := map[string]any{
		"prompt": map[string]string{"text": prompt},
	}
	if modelID != "" {
		body["model"] = map[string]string{"id": modelID}
	}
	if r.RepoURL != "" {
		body["repos"] = []map[string]string{{"url": r.RepoURL}}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("cursor: marshal body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/v1/agents", bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("cursor: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+r.APIKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("cursor: create agent: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respRaw, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode >= 400 {
		return "", "", fmt.Errorf("cursor: create agent HTTP %d: %s", httpResp.StatusCode, string(respRaw))
	}

	var agentResp struct {
		ID          string `json:"id"`
		LatestRunID string `json:"latestRunId"`
	}
	if err := json.Unmarshal(respRaw, &agentResp); err != nil {
		return "", "", fmt.Errorf("cursor: parse agent response: %w", err)
	}
	if agentResp.ID == "" || agentResp.LatestRunID == "" {
		return "", "", fmt.Errorf("cursor: agent response missing id or latestRunId: %s", string(respRaw))
	}
	return agentResp.ID, agentResp.LatestRunID, nil
}

// streamRun connects to the SSE stream for a run and accumulates the output.
// Returns the final result text (from the "result" event) or the accumulated
// assistant deltas, and any error encountered.
func (r *CursorRunner) streamRun(ctx context.Context, agentID, runID string, emit func(level, msg string)) (string, error) {
	url := fmt.Sprintf("%s/v1/agents/%s/runs/%s/stream", r.BaseURL, agentID, runID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("cursor: build stream request: %w", err)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+r.APIKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("cursor: stream: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode >= 400 {
		raw, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("cursor: stream HTTP %d: %s", httpResp.StatusCode, string(raw))
	}

	var (
		textBuf     strings.Builder
		finalResult string
		runErr      error
		eventType   string
		dataLines   []string
	)

	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "")
		dataLines = nil

		switch eventType {
		case "assistant":
			var ev struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta != "" {
				textBuf.WriteString(ev.Delta)
				emit("debug", "[assistant] "+ev.Delta)
			}
		case "thinking":
			var ev struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Delta != "" {
				emit("debug", "[thinking] "+ev.Delta)
			}
		case "tool_call":
			emit("debug", "[tool_call] "+data)
		case "result":
			var ev struct {
				Status string `json:"status"`
				Result string `json:"result"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				finalResult = ev.Result
				emit("debug", fmt.Sprintf("[result:%s]", ev.Status))
			}
		case "error":
			var ev struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil && ev.Message != "" {
				runErr = fmt.Errorf("cursor: agent error: %s", ev.Message)
			} else {
				runErr = fmt.Errorf("cursor: agent error: %s", data)
			}
		}
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if runErr != nil || ctx.Err() != nil {
			break
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			savedType := eventType
			flush()
			eventType = ""
			if savedType == "done" {
				goto streamDone
			}
		}
	}

streamDone:
	if ctx.Err() != nil {
		return textBuf.String(), ctx.Err()
	}
	if runErr != nil {
		return textBuf.String(), runErr
	}
	if err := scanner.Err(); err != nil {
		return textBuf.String(), fmt.Errorf("cursor: stream scan: %w", err)
	}
	if finalResult != "" {
		return finalResult, nil
	}
	return textBuf.String(), nil
}
