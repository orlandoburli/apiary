package main

import (
	"context"
	"os"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

func main() {
	pluginsdk.Main(export)
}

func export(_ context.Context, req pluginsdk.Request) (any, *pluginsdk.ResponseError) {
	if req.Capability != pluginsdk.CapabilityEventExporter || req.Method != "export" {
		return nil, &pluginsdk.ResponseError{Code: "unsupported_method", Message: "expected event_exporter.export"}
	}
	path, _ := req.Config["path"].(string)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, &pluginsdk.ResponseError{Code: "write_failed", Message: err.Error()}
	}
	_, err = file.Write(append(req.Payload, '\n'))
	closeErr := file.Close()
	if err != nil {
		return nil, &pluginsdk.ResponseError{Code: "write_failed", Message: err.Error()}
	}
	if closeErr != nil {
		return nil, &pluginsdk.ResponseError{Code: "close_failed", Message: closeErr.Error()}
	}
	return map[string]any{"written": true}, nil
}
