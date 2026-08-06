// Command apiary-plugin-source-file is the reference protocol-1 source
// plugin: it polls work items from a JSON file, so any external process can
// drop items there and have Apiary dispatch workflows for them. It shows the
// full CapabilitySource contract — poll returns the file's items each cycle
// (stable IDs make re-dispatch impossible downstream), acknowledge and
// write_result are honest no-ops.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

func main() {
	pluginsdk.Main(serve)
}

func serve(_ context.Context, req pluginsdk.Request) (any, *pluginsdk.ResponseError) {
	if req.Capability != pluginsdk.CapabilitySource {
		return nil, &pluginsdk.ResponseError{Code: "unsupported_capability", Message: "expected capability source"}
	}
	switch req.Method {
	case pluginsdk.SourceMethodPoll:
		return poll(req)
	case pluginsdk.SourceMethodAcknowledge, pluginsdk.SourceMethodWriteResult:
		// Nothing to mark in a plain file; report success so the host logs stay clean.
		return pluginsdk.SourceOKResult{OK: true}, nil
	default:
		return nil, &pluginsdk.ResponseError{Code: "unsupported_method", Message: fmt.Sprintf("unknown method %q", req.Method)}
	}
}

func poll(req pluginsdk.Request) (any, *pluginsdk.ResponseError) {
	path, _ := req.Config["path"].(string)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// An absent file simply means no work yet.
		return pluginsdk.SourcePollResult{Items: []pluginsdk.SourceItem{}}, nil
	}
	if err != nil {
		return nil, &pluginsdk.ResponseError{Code: "read_failed", Message: err.Error()}
	}
	var items []pluginsdk.SourceItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &pluginsdk.ResponseError{Code: "invalid_items", Message: fmt.Sprintf("%s must hold a JSON array of items: %v", path, err)}
	}
	return pluginsdk.SourcePollResult{Items: items}, nil
}
