// Package mcp implements the small stdio MCP server apiary ships for its own
// agents. It speaks newline-delimited JSON-RPC 2.0 directly rather than pulling
// in an SDK: the surface is one tool, and the transport is a file descriptor.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	// daemonPollWait is the daemon-side long-poll length. The daemon caps it at
	// 30s, so 20 leaves room for a slow answer to land between polls.
	daemonPollWait = 20
	// askDeadline bounds the whole conversation. The daemon itself expires an
	// unanswered question after 30 minutes, so an agent never sits forever even
	// if the human never looks; this is the client-side backstop in case the
	// daemon dies mid-poll.
	askDeadline = 30 * time.Minute
	// pollBackoff keeps a daemon that answers "pending" immediately from turning
	// the loop into a busy spin. Normal long-polls sleep server-side anyway.
	pollBackoff = 250 * time.Millisecond
)

// QuestionOption is one answer the agent offers. It mirrors the daemon payload
// so the MCP schema and the HTTP body cannot drift.
type QuestionOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Answer is the human's reply, or the fact that none arrived.
type Answer struct {
	Answer     string
	Reason     string
	AnsweredBy string
	TimedOut   bool
}

// DaemonClient talks to the apiary daemon over its Unix socket. The per-run ids
// are read from the environment at construction: the MCP config is written once
// per provider, but these values are injected per run by the daemon, so the MCP
// process must pick them up from its inherited environment.
type DaemonClient struct {
	SocketPath string
	// Deadline bounds Ask. Exposed so tests can use a short clock.
	Deadline time.Duration

	InstanceID string
	StepID     string
	TaskID     string
	AgentID    string

	http *http.Client
}

// NewDaemonClient builds a client whose HTTP transport dials the daemon's Unix
// socket. The host name in the URL is arbitrary; the DialContext ignores it.
func NewDaemonClient(socketPath string) *DaemonClient {
	if socketPath == "" {
		socketPath = os.Getenv("APIARY_SOCKET")
	}
	return &DaemonClient{
		SocketPath: socketPath,
		Deadline:   askDeadline,
		InstanceID: os.Getenv("APIARY_INSTANCE_ID"),
		StepID:     os.Getenv("APIARY_STEP_ID"),
		TaskID:     os.Getenv("APIARY_TASK_ID"),
		AgentID:    os.Getenv("APIARY_AGENT_ID"),
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}},
	}
}

type createQuestionBody struct {
	WorkflowInstanceID string           `json:"workflow_instance_id,omitempty"`
	StepID             string           `json:"step_id,omitempty"`
	TaskID             string           `json:"task_id,omitempty"`
	AgentID            string           `json:"agent_id,omitempty"`
	Question           string           `json:"question"`
	Options            []QuestionOption `json:"options,omitempty"`
	AllowFreeText      bool             `json:"allow_free_text"`
}

type pollResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Answer     string `json:"answer"`
	Reason     string `json:"reason"`
	AnsweredBy string `json:"answered_by"`
}

// Ask posts the question, then long-polls until it is answered, expires, or the
// deadline passes. A timeout is returned as an Answer, not an error: the agent
// should continue with its best judgement rather than crash or wedge because a
// human stepped away.
func (c *DaemonClient) Ask(ctx context.Context, question string, options []QuestionOption, allowFreeText bool) (Answer, error) {
	if c.http == nil {
		return Answer{}, fmt.Errorf("mcp: daemon client is not initialized")
	}
	body, err := json.Marshal(createQuestionBody{
		WorkflowInstanceID: c.InstanceID,
		StepID:             c.StepID,
		TaskID:             c.TaskID,
		AgentID:            c.AgentID,
		Question:           question,
		Options:            options,
		AllowFreeText:      allowFreeText,
	})
	if err != nil {
		return Answer{}, err
	}
	var created struct {
		ID string `json:"id"`
	}
	postCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = c.do(postCtx, http.MethodPost, "/questions", body, &created)
	cancel()
	if err != nil {
		return Answer{}, err
	}
	if created.ID == "" {
		return Answer{}, fmt.Errorf("mcp: daemon did not return a question id")
	}

	deadline := time.Now().Add(askDeadline)
	if c.Deadline > 0 {
		deadline = time.Now().Add(c.Deadline)
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Answer{TimedOut: true}, nil
		}
		wait := daemonPollWait
		if remaining < time.Duration(wait)*time.Second {
			// At least one second: a wait=0 would be normalized to the default
			// 20s by the daemon, overrunning our deadline.
			wait = int((remaining + time.Second - 1) / time.Second)
			if wait < 1 {
				wait = 1
			}
		}
		var got pollResponse
		path := "/questions/" + url.PathEscape(created.ID) + "?wait=" + fmt.Sprint(wait)
		// A per-poll timeout bounds a daemon that accepts the request and then
		// goes silent; the wall-clock deadline alone would never fire while a
		// read is stuck.
		pollCtx, cancel := context.WithTimeout(ctx, time.Duration(wait+5)*time.Second)
		err := c.do(pollCtx, http.MethodGet, path, nil, &got)
		cancel()
		if err != nil {
			return Answer{}, err
		}
		switch got.Status {
		case "answered":
			return Answer{Answer: got.Answer, Reason: got.Reason, AnsweredBy: got.AnsweredBy}, nil
		case "timed_out", "cancelled":
			return Answer{TimedOut: true}, nil
		}
		select {
		case <-ctx.Done():
			return Answer{}, ctx.Err()
		case <-time.After(pollBackoff):
		}
	}
}

func (c *DaemonClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://apiary"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("mcp: daemon %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
