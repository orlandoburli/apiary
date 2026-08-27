package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeOneEchoesRequestAndStructuredErrors(t *testing.T) {
	input := strings.NewReader(`{"protocol":1,"request_id":"req-1","capability":"event_exporter","method":"export","payload":{"type":"test"}}`)
	var output bytes.Buffer
	err := ServeOne(context.Background(), input, &output, func(_ context.Context, request Request) (any, *ResponseError) {
		if request.Capability != CapabilityEventExporter || request.Method != "export" {
			t.Fatalf("request=%+v", request)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"accepted": payload["type"]}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Protocol != ProtocolVersion || response.RequestID != "req-1" || response.Error != nil {
		t.Fatalf("response=%+v", response)
	}
}

func TestServeOneRejectsProtocolMismatchWithoutCallingHandler(t *testing.T) {
	input := strings.NewReader(`{"protocol":99,"request_id":"req-2","capability":"runner","method":"run"}`)
	var output bytes.Buffer
	called := false
	if err := ServeOne(context.Background(), input, &output, func(context.Context, Request) (any, *ResponseError) { called = true; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("handler called for unsupported protocol")
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != "unsupported_protocol" {
		t.Fatalf("response=%+v", response)
	}
}

func TestServeOneRejectsTrailingDataWithoutAnswering(t *testing.T) {
	// The host starts one process per request, so a second object glued onto
	// the stream means the framing is broken — the first request must not be
	// answered as if the exchange were healthy.
	input := strings.NewReader(`{"protocol":1,"request_id":"req-1","capability":"source","method":"poll"}` + "\n" +
		`{"protocol":1,"request_id":"req-2","capability":"source","method":"poll"}`)
	var output bytes.Buffer
	called := false
	err := ServeOne(context.Background(), input, &output, func(context.Context, Request) (any, *ResponseError) {
		called = true
		return SourceOKResult{OK: true}, nil
	})
	if err == nil {
		t.Fatal("trailing data must be a transport error")
	}
	if called {
		t.Fatal("handler must not run for a malformed stream")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", output.String())
	}
}

func TestServeOneAcceptsTrailingWhitespace(t *testing.T) {
	// Encoders that terminate the request with a newline (or several) are
	// fine: only another *value* counts as trailing data.
	input := strings.NewReader(`{"protocol":1,"request_id":"req-1","capability":"source","method":"poll"}` + "\n\n  \t\n")
	var output bytes.Buffer
	if err := ServeOne(context.Background(), input, &output, func(context.Context, Request) (any, *ResponseError) {
		return SourceOKResult{OK: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "req-1" || response.Error != nil {
		t.Fatalf("response=%+v", response)
	}
}
