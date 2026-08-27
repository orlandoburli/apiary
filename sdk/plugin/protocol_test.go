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
