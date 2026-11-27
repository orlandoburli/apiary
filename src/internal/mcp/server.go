package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/orlandoburli/apiary/internal/version"
)

// Protocol version we advertise. Agents negotiate against this; 2024-11-05 is
// the revision whose stdio framing (newline-delimited JSON) this server speaks.
const protocolVersion = "2024-11-05"

const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Server is a newline-delimited JSON-RPC 2.0 MCP server. Requests are handled
// one at a time: tools/call deliberately blocks until the human answers, and the
// agent is waiting on that answer anyway.
type Server struct {
	in  io.Reader
	out io.Writer
	ask func(context.Context, string, []QuestionOption, bool) (Answer, error)
	mu  sync.Mutex
}

// NewServer wires the server to a transport. Injecting the ask function keeps
// the framing tests independent of any daemon.
func NewServer(in io.Reader, out io.Writer, client *DaemonClient) *Server {
	return &Server{
		in:  in,
		out: out,
		ask: func(ctx context.Context, question string, options []QuestionOption, allowFreeText bool) (Answer, error) {
			if client == nil {
				return Answer{}, fmt.Errorf("mcp: no daemon client configured")
			}
			return client.Ask(ctx, question, options, allowFreeText)
		},
	}
}

// Serve reads JSON-RPC messages until stdin closes or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	reader := bufio.NewReader(s.in)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			s.handleLine(ctx, []byte(trimmed))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(nil, errParse, "parse error")
		return
	}
	// No id means a notification: JSON-RPC forbids a response, and
	// notifications/initialized is exactly that case.
	if len(req.ID) == 0 {
		return
	}
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "apiary", "version": version.Version},
		})
	case "ping":
		s.writeResult(req.ID, map[string]any{})
	case "tools/list":
		s.writeResult(req.ID, map[string]any{"tools": []any{askHumanTool()}})
	case "tools/call":
		s.handleToolCall(ctx, req)
	case "notifications/initialized":
		// Defensive: some clients send this with an id. Nothing to do.
		s.writeResult(req.ID, map[string]any{})
	default:
		s.writeError(req.ID, errMethodNotFound, "method not found: "+req.Method)
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type askHumanArgs struct {
	Question      string           `json:"question"`
	Options       []QuestionOption `json:"options"`
	AllowFreeText *bool            `json:"allow_free_text"`
}

func (s *Server) handleToolCall(ctx context.Context, req rpcRequest) {
	var params callParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, errInvalidParams, "invalid tools/call params")
		return
	}
	if params.Name != "ask_human" {
		s.writeError(req.ID, errInvalidParams, "unknown tool: "+params.Name)
		return
	}
	var args askHumanArgs
	if err := json.Unmarshal(params.Arguments, &args); err != nil {
		s.writeError(req.ID, errInvalidParams, "invalid ask_human arguments")
		return
	}
	if strings.TrimSpace(args.Question) == "" {
		s.writeError(req.ID, errInvalidParams, "question is required")
		return
	}
	allowFreeText := true
	if args.AllowFreeText != nil {
		allowFreeText = *args.AllowFreeText
	}

	answer, err := s.ask(ctx, args.Question, args.Options, allowFreeText)
	if err != nil {
		s.writeResult(req.ID, toolResult{
			Content: []toolContent{{Type: "text", Text: "ask_human failed: " + err.Error()}},
			IsError: true,
		})
		return
	}
	if answer.TimedOut {
		// A timeout is not an error: the agent must continue. Returning isError
		// would make some clients abort the step, which is the opposite of what a
		// human who stepped away needs.
		s.writeResult(req.ID, toolResult{
			Content: []toolContent{{Type: "text", Text: "No answer arrived before the deadline; continue without it."}},
			IsError: false,
		})
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"answer":      answer.Answer,
		"reason":      answer.Reason,
		"answered_by": answer.AnsweredBy,
	})
	s.writeResult(req.ID, toolResult{
		Content: []toolContent{{Type: "text", Text: string(payload)}},
	})
}

// askHumanTool is the single tool this server exposes. The schema is written as
// data because MCP clients read it verbatim; the implementation above is the
// only thing that needs to stay in sync with it.
func askHumanTool() map[string]any {
	return map[string]any{
		"name":        "ask_human",
		"description": "Ask a human a question and block until they answer or the question expires. Use this when a decision needs a person; continue with your best judgement if no answer arrives.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask, with enough context for someone who has not read the run's history.",
				},
				"options": map[string]any{
					"type":        "array",
					"description": "Answers the human can pick from. Omit for a free-text question.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{"type": "string"},
							"label": map[string]any{"type": "string"},
						},
						"required": []any{"value"},
					},
				},
				"allow_free_text": map[string]any{
					"type":        "boolean",
					"description": "Whether a typed answer is accepted in addition to the options. Defaults to true.",
					"default":     true,
				},
			},
			"required": []any{"question"},
		},
	}
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) write(resp rpcResponse) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(append(raw, '\n'))
}
