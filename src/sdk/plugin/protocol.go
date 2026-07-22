// Package plugin is the public SDK for Apiary's out-of-process JSON protocol.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const ProtocolVersion = 1

type Capability string

const (
	CapabilitySource           Capability = "source"
	CapabilityRunner           Capability = "runner"
	CapabilityWorkflowAction   Capability = "workflow_action"
	CapabilityApprovalProvider Capability = "approval_provider"
	CapabilitySecretProvider   Capability = "secret_provider"
	CapabilityEventExporter    Capability = "event_exporter"
)

type Request struct {
	Protocol   int             `json:"protocol"`
	RequestID  string          `json:"request_id"`
	Capability Capability      `json:"capability"`
	Method     string          `json:"method"`
	Config     map[string]any  `json:"config,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Protocol  int            `json:"protocol"`
	RequestID string         `json:"request_id"`
	Result    any            `json:"result,omitempty"`
	Error     *ResponseError `json:"error,omitempty"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Handler func(context.Context, Request) (any, *ResponseError)

// ServeOne decodes one request, invokes handler, and writes one response. It is
// intentionally single-shot because the host starts a fresh process for every
// isolated invocation.
func ServeOne(ctx context.Context, input io.Reader, output io.Writer, handler Handler) error {
	if handler == nil {
		return fmt.Errorf("plugin handler is required")
	}
	var request Request
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	response := Response{Protocol: ProtocolVersion, RequestID: request.RequestID}
	if request.Protocol != ProtocolVersion {
		response.Error = &ResponseError{Code: "unsupported_protocol", Message: fmt.Sprintf("protocol %d is unsupported; expected %d", request.Protocol, ProtocolVersion)}
	} else {
		response.Result, response.Error = handler(ctx, request)
	}
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// Main serves one request on stdin/stdout and writes startup/protocol failures
// to stderr with a non-zero exit status. Plugin executables can call Main from
// their main function.
func Main(handler Handler) {
	if err := ServeOne(context.Background(), os.Stdin, os.Stdout, handler); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
