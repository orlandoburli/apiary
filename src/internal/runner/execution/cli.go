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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// CliRunner manages a CLI subprocess with stdout/stderr streaming, PID tracking,
// and heartbeats. Used by claude, opencode-cli, and similar providers.
type CliRunner struct {
	command          string
	args             []string
	modelFlag        string
	promptFlag       string
	turnsFlag        string
	promptPositional bool // pass prompt as last positional arg instead of a flag
	// mcpFormat selects how MCP servers are serialised into the provider's native
	// config ("claude" | "cursor" | "opencode"); empty disables MCP support.
	mcpFormat string
	// mcps are the resolved (runner + agent) MCP servers for this runner instance.
	mcps []model.MCPServer
	// mcpRunArgs are extra CLI args injected into every run to activate the MCP
	// config written by setupMCP (e.g. claude's "--mcp-config <path>").
	mcpRunArgs []string
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
	if v, ok := config["prompt_positional"].(bool); ok {
		r.promptPositional = v
	}
	if raw, ok := config["args"].([]any); ok {
		for _, a := range raw {
			if s, ok := a.(string); ok {
				r.args = append(r.args, s)
			}
		}
	}
	if v, ok := config["mcp_format"].(string); ok && v != "" {
		r.mcpFormat = v
	}
	if v, ok := config["mcps"].([]model.MCPServer); ok {
		r.mcps = v
	}
	// Materialise the provider's MCP config (and any per-run CLI args) once the
	// servers are known. Configure runs at load time, sequentially across agents,
	// so the global-config merges (cursor/opencode) are race-free.
	if len(r.mcps) > 0 {
		args, err := r.setupMCP()
		if err != nil {
			return fmt.Errorf("cli runner: setup MCP: %w", err)
		}
		r.mcpRunArgs = args
	}
	return nil
}

func (r *CliRunner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	start := time.Now()
	prompt := buildPrompt(req)

	argv := append([]string{}, r.args...)
	// Inject MCP activation args (e.g. claude's "--mcp-config <path>") before the
	// model/prompt flags; the provider config itself was written by setupMCP.
	argv = append(argv, r.mcpRunArgs...)
	if r.modelFlag != "" && req.Model != "" {
		argv = append(argv, r.modelFlag, req.Model)
	}
	if r.turnsFlag != "" && req.MaxTurns > 0 {
		argv = append(argv, r.turnsFlag, fmt.Sprintf("%d", req.MaxTurns))
	}
	if r.promptPositional {
		argv = append(argv, prompt)
	} else if r.promptFlag != "" {
		argv = append(argv, r.promptFlag, prompt)
	}

	cmd := exec.CommandContext(ctx, r.command, argv...)
	cmd.Dir = req.WorkingDir
	cmd.Env = hostEnv()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if r.promptFlag == "" && !r.promptPositional {
		cmd.Stdin = strings.NewReader(prompt)
	}

	var (
		mu          sync.Mutex
		outBuf      bytes.Buffer
		errBuf      bytes.Buffer
		logs        []model.LogEntry
		finalResult string
		usage       model.Usage
		// Set when the provider emits a rate_limit_event with status "rejected"
		// (e.g. Claude's 5-hour session limit). resetsAt is epoch seconds, 0 if
		// the provider did not report it. Guarded by mu (written from the stdout
		// goroutine).
		rateLimited       bool
		rateLimitResetsAt int64
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
			if req.TranscriptSink != nil {
				req.TranscriptSink(line)
			}
			if res, ok := finalResultText(line); ok {
				mu.Lock()
				finalResult = res
				mu.Unlock()
			}
			if resetsAt, rejected := detectRateLimitRejection(line); rejected {
				mu.Lock()
				rateLimited = true
				if resetsAt > 0 {
					rateLimitResetsAt = resetsAt
				}
				mu.Unlock()
			}
			accumulateStreamUsage(line, &usage)
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

	usagePtr := &usage
	if usage.TotalTokens == 0 && usage.NumTurns == 0 && usage.CostUSD == 0 {
		usagePtr = nil
	}
	result := model.RunResult{
		WorkerID:    req.WorkerID,
		Success:     runErr == nil,
		Output:      output,
		Logs:        logs,
		Duration:    time.Since(start),
		Usage:       usagePtr,
		InputPrompt: prompt,
		RateLimited: rateLimited,
	}
	if rateLimited && rateLimitResetsAt > 0 {
		result.RateLimitResetsAt = time.Unix(rateLimitResetsAt, 0)
	}
	if runErr != nil {
		// claude often exits non-zero with an empty stderr, leaving a bare
		// "exit status 1" with no clue why. Fall back to the most meaningful
		// stdout (final result / last assistant text) so the recorded error is
		// diagnosable instead of opaque.
		detail := strings.TrimSpace(errBuf.String())
		if detail == "" {
			detail = errorDetail(output)
		}
		if detail != "" {
			result.Error = fmt.Errorf("%w: %s", runErr, detail)
		} else {
			result.Error = runErr
		}
	}
	// Classify failure via the generic (or registered) failure detector.
	// This sets FailureKind, CreditExhausted, etc. based on exit code, output
	// content, and known error patterns — beyond the narrow rate_limit_event.
	detector := FailureDetectorFor(r.ID())
	if kind, resetsAt := detector.Detect(req, &result); kind != model.FailureNone {
		result.FailureKind = kind
		switch kind {
		case model.FailureRateLimited:
			result.RateLimited = true
			if !resetsAt.IsZero() {
				result.RateLimitResetsAt = resetsAt
			}
		case model.FailureCreditExhausted:
			result.CreditExhausted = true
			result.Success = false
			if !resetsAt.IsZero() && result.RateLimitResetsAt.IsZero() {
				result.RateLimitResetsAt = resetsAt
			}
		case model.FailureAborted:
			result.Success = false
		}
	}

	// Extract APIARY_OUTPUT / APIARY_SUMMARY sentinels into structured fields.
	applyStructured(&result)
	return result, nil
}

// cliUsage mirrors the usage object the CLIs report on `assistant` message
// events and the final `result` event. It accepts both the Claude CLI's
// snake_case fields (input_tokens, cache_creation_input_tokens, …) and the
// Cursor agent CLI's camelCase fields (inputTokens, cacheWriteTokens, …); only
// one naming is present per event, so the accessors sum the pair. Cache tokens
// are real input the model processed (and are billed), so inputTotal folds them
// into the input side; cacheCreation/cacheRead expose the breakdown separately.
type cliUsage struct {
	InputTokens              int `json:"input_tokens"`
	InputTokensCamel         int `json:"inputTokens"`
	OutputTokens             int `json:"output_tokens"`
	OutputTokensCamel        int `json:"outputTokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheWriteTokensCamel    int `json:"cacheWriteTokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheReadTokensCamel     int `json:"cacheReadTokens"`
}

func (u cliUsage) input() int         { return u.InputTokens + u.InputTokensCamel }
func (u cliUsage) output() int        { return u.OutputTokens + u.OutputTokensCamel }
func (u cliUsage) cacheCreation() int { return u.CacheCreationInputTokens + u.CacheWriteTokensCamel }
func (u cliUsage) cacheRead() int     { return u.CacheReadInputTokens + u.CacheReadTokensCamel }

func (u cliUsage) inputTotal() int {
	return u.input() + u.cacheCreation() + u.cacheRead()
}

// stream-json event types (Claude CLI output format). The CLI emits complete-
// message events — `system`, `assistant`, `user`, `result` — not the raw
// Anthropic API's `message_start`/`message_delta` token deltas. Usage rides on
// the `assistant` message and (authoritatively) on the final `result` event.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Result  string `json:"result"`
	Index   int    `json:"index"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
		Usage *cliUsage `json:"usage"`
	} `json:"message"`
	Usage        *cliUsage `json:"usage"` // present on the `result` event
	DurationMs   int64     `json:"duration_ms"`
	NumTurns     int       `json:"num_turns"`
	TotalCostUSD float64   `json:"total_cost_usd"`
	IsError      bool      `json:"is_error"`
}

// accumulateStreamUsage folds one stream-json line into the running usage. Token
// totals come from the final `result` event's usage — authoritative, matches
// total_cost_usd, and includes cache tokens. Tool calls are counted from the
// `tool_use` blocks in `assistant` messages. The per-assistant usage is also
// recorded as a fallback so a run that dies before the `result` event still
// reports the last message's counts rather than zeros.
// applyUsage records a parsed CLI usage object onto the running totals.
// InputTokens stays the full billed input (cache folded in) so totals and the
// existing in/out/total displays are unchanged; the cache breakdown is recorded
// separately. TotalTokens = full input + output (cache already lives in input).
func applyUsage(u *model.Usage, cu *cliUsage) {
	u.InputTokens = cu.inputTotal()
	u.OutputTokens = cu.output()
	u.CacheCreationTokens = cu.cacheCreation()
	u.CacheReadTokens = cu.cacheRead()
	u.TotalTokens = u.InputTokens + u.OutputTokens
}

func accumulateStreamUsage(line string, u *model.Usage) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type == "" {
		return
	}
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" {
				u.NumToolCalls++
			}
		}
		if ev.Message.Usage != nil {
			applyUsage(u, ev.Message.Usage)
		}
	case "result":
		u.NumTurns = ev.NumTurns
		if ev.TotalCostUSD > 0 {
			u.CostUSD = ev.TotalCostUSD
		}
		if ev.Usage != nil { // authoritative cumulative totals
			applyUsage(u, ev.Usage)
		}
	}
}

// rateLimitEvent is the provider's usage-limit signal in the stream. Claude
// emits it as {"type":"rate_limit_event","rate_limit_info":{"status":"rejected",
// "resetsAt":<epoch-seconds>,...}} when a run is blocked by the 5-hour session
// limit (the run then prints "you've hit your session limit" and may exit 0).
type rateLimitEvent struct {
	Type          string `json:"type"`
	RateLimitInfo *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

// detectRateLimitRejection reports whether a stream-json line is a
// rate_limit_event with status "rejected" (the provider refused the run because
// of a usage limit), along with the epoch-seconds reset time when present.
// Other statuses ("allowed", "allowed_warning") are not rejections.
func detectRateLimitRejection(line string) (resetsAt int64, rejected bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return 0, false
	}
	var ev rateLimitEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return 0, false
	}
	if ev.Type != "rate_limit_event" || ev.RateLimitInfo == nil {
		return 0, false
	}
	if ev.RateLimitInfo.Status != "rejected" {
		return 0, false
	}
	return ev.RateLimitInfo.ResetsAt, true
}

// errorDetail trims, single-lines, and length-caps an output fragment so it can
// be folded into a recorded error message without dumping a whole transcript.
func errorDetail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	const max = 300
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return s
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
			(time.Duration(ev.DurationMs) * time.Millisecond).Round(time.Millisecond))
		if ev.TotalCostUSD > 0 {
			out += fmt.Sprintf(" cost=$%.4f", ev.TotalCostUSD)
		}
		if r := strings.TrimSpace(ev.Result); r != "" {
			out += "\n" + r
		}
		return out, true
	// cursor agent CLI emits {"type":"completion","result":"...","is_error":false}
	case "completion":
		status := "success"
		if ev.IsError {
			status = "error"
		}
		out := fmt.Sprintf("[completion:%s]", status)
		if r := strings.TrimSpace(ev.Result); r != "" {
			out += "\n" + r
		}
		return out, true
	}
	return "", false
}

func finalResultText(line string) (string, bool) {
	if !strings.Contains(line, `"type":"result"`) && !strings.Contains(line, `"type":"completion"`) {
		return "", false
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
		return "", false
	}
	if ev.Type != "result" && ev.Type != "completion" {
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
	if req.SystemPrepend != "" {
		b.WriteString(req.SystemPrepend)
		b.WriteString("\n\n")
	}
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
	if req.OutputInstruction != "" {
		b.WriteString(req.OutputInstruction)
	}
	if req.SummaryPrompt != "" {
		b.WriteString(summaryInstruction(req.SummaryPrompt))
	}
	return b.String()
}
