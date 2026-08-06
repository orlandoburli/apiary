# Plugin SDK

Plugins talk to Apiary over a deliberately small JSON protocol (see
[Protocol version 1](plugins.md#protocol-version-1)), so a plugin can be
written in **any language**. For Go there is an official SDK that handles the
protocol envelope for you; for everything else this page specifies exactly
what your code — or an SDK you build for your language — must do.

## Go SDK

Import path:

```go
import pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
```

The SDK lives in the main Apiary module (`src/sdk/plugin`) and has no
dependencies beyond the standard library.

### Entry points

| Symbol | Purpose |
|---|---|
| `Main(handler Handler)` | Call from `main()`: serves one request on stdin/stdout, writes protocol failures to stderr, exits non-zero on transport errors. This is all a plugin `main` needs |
| `ServeOne(ctx, in, out, handler) error` | The engine behind `Main`, with injectable reader/writer — use it in tests |
| `Handler` | `func(context.Context, Request) (any, *ResponseError)` — your plugin logic. Return `(result, nil)` for success or `(nil, &ResponseError{...})` for failure |

The SDK decodes the single request (rejecting unknown envelope fields),
refuses protocol mismatches with a well-formed `error` response, invokes your
handler, echoes the request ID, and encodes the response — the whole
[transport contract](plugins.md#protocol-version-1), so your handler only
sees typed data.

### Request and response types

```go
type Request struct {
    Protocol   int             // always 1 once your handler runs
    RequestID  string          // echoed for you — no need to touch it
    Capability Capability      // "source", "event_exporter", …
    Method     string          // e.g. "poll"
    Config     map[string]any  // your instance's config: block from apiary.yaml
    Payload    json.RawMessage // method input — unmarshal into the typed structs below
}

type ResponseError struct {
    Code    string // machine-readable, e.g. "unsupported_method", "read_failed"
    Message string // human-readable
}
```

Capability constants: `CapabilitySource`, `CapabilityEventExporter` (live),
plus `CapabilityRunner`, `CapabilityWorkflowAction`,
`CapabilityApprovalProvider`, `CapabilitySecretProvider` (reserved).

### Source plugin types

For `capability: source` the SDK defines the full wire contract in
`sdk/plugin/source.go` — use these instead of hand-rolling maps:

| Symbol | Role |
|---|---|
| `SourceMethodPoll`, `SourceMethodAcknowledge`, `SourceMethodWriteResult` | Method name constants to switch on |
| `SourcePollRequest` | Poll payload: `Since` (RFC3339, informational), `States`/`Labels` (the source's `filters:`, forwarded for backend-side filtering) |
| `SourcePollResult` | Your poll answer: `Items []SourceItem` |
| `SourceItem` | One work item. `ID` is **required and is the dedup key** — keep it stable per dispatch-worthy occurrence; items without it are dropped. `Labels` use `key:value` form and drive trigger matching. Timestamps are RFC3339 strings; empty means "now" |
| `SourceAckRequest`, `SourceWriteResultRequest` | Payloads of the two write-back methods |
| `SourceOKResult` | The conventional `{"ok": true}` answer when there is nothing to mark |

### A complete source plugin in Go

This is the bundled reference plugin (`src/examples/plugins/source-file`),
trimmed to its essence:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "os"

    pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

func main() { pluginsdk.Main(serve) }

func serve(_ context.Context, req pluginsdk.Request) (any, *pluginsdk.ResponseError) {
    if req.Capability != pluginsdk.CapabilitySource {
        return nil, &pluginsdk.ResponseError{Code: "unsupported_capability", Message: "expected capability source"}
    }
    switch req.Method {
    case pluginsdk.SourceMethodPoll:
        path, _ := req.Config["path"].(string)
        raw, err := os.ReadFile(path)
        if os.IsNotExist(err) {
            return pluginsdk.SourcePollResult{Items: []pluginsdk.SourceItem{}}, nil
        }
        if err != nil {
            return nil, &pluginsdk.ResponseError{Code: "read_failed", Message: err.Error()}
        }
        var items []pluginsdk.SourceItem
        if err := json.Unmarshal(raw, &items); err != nil {
            return nil, &pluginsdk.ResponseError{Code: "invalid_items", Message: err.Error()}
        }
        return pluginsdk.SourcePollResult{Items: items}, nil
    case pluginsdk.SourceMethodAcknowledge, pluginsdk.SourceMethodWriteResult:
        return pluginsdk.SourceOKResult{OK: true}, nil
    default:
        return nil, &pluginsdk.ResponseError{Code: "unsupported_method", Message: fmt.Sprintf("unknown method %q", req.Method)}
    }
}
```

Build it as a static binary, pair it with a manifest declaring
`"capabilities": ["source"]`, and [install it](plugins.md#installing-a-plugin).

### Testing without Apiary

The protocol is pipeable, so a shell one-liner is a complete integration test:

```bash
echo '{"protocol":1,"request_id":"t1","capability":"source","method":"poll","config":{"path":"items.json"}}' \
  | ./apiary-plugin-source-file
```

In Go tests, drive `ServeOne` with a `strings.Reader` and a `bytes.Buffer` —
no subprocess needed.

## Other languages

There is no official SDK outside Go yet (tracked in
[#367](https://github.com/orlandoburli/apiary/issues/367)) — but none is
required. Complete installable examples ship in `src/examples/plugins/`:
`source-bash` (shell script, no build step) and `source-node`
(TypeScript compiled to a shebang executable), both behaviorally identical to
the Go `source-file` plugin. The protocol is small enough to implement
directly; the [Python plugin in the protocol docs](plugins.md#a-complete-plugin-in-20-lines)
is 20 lines with no dependencies.

### Bash example

The floor is genuinely low: a plugin can be a **shell script** — the manifest's
`executable` only needs to be executable. With `jq` for the JSON handling, a
complete `source` plugin that surfaces failed launchd/systemd-style services
(here: a hard-coded item) is:

```bash
#!/usr/bin/env bash
set -euo pipefail

req=$(cat)                                       # the single request, stdin → EOF
request_id=$(jq -r '.request_id' <<<"$req")
method=$(jq -r '.method' <<<"$req")

respond() { jq -nc --arg id "$request_id" "{protocol: 1, request_id: \$id} + $1"; }

case "$method" in
  poll)
    echo "checking services..." >&2               # diagnostics → stderr only
    respond '{result: {items: [{
      id: "sh-1",
      title: "Hello from Bash",
      labels: ["origin:bash"],
      state: "open"
    }]}}'
    ;;
  acknowledge|write_result)
    respond '{result: {ok: true}}'
    ;;
  *)
    respond "{error: {code: \"unsupported_method\", message: \"unknown method $method\"}}"
    ;;
esac
```

Real-world versions swap the hard-coded item for `curl`/`jq` against whatever
API you monitor. Mind stdout purity: every `echo` that isn't the response must
go to stderr (`>&2`). A complete, installable Bash plugin (config handling,
error responses, file-backed items) ships as
`src/examples/plugins/source-bash`.

### Rust example

A minimal `source` plugin in Rust (verified against the daemon; `serde_json`
is the only dependency):

```toml
# Cargo.toml
[package]
name = "apiary-plugin-rust-source"
version = "1.0.0"
edition = "2021"

[dependencies]
serde_json = "1"
```

```rust
// src/main.rs
use serde_json::{json, Value};
use std::io::Read;

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).expect("read stdin");
    let req: Value = serde_json::from_str(&input).expect("decode request");
    let request_id = req["request_id"].as_str().unwrap_or_default();

    let body = if req["protocol"] != json!(1) {
        json!({"error": {"code": "unsupported_protocol", "message": "expected protocol 1"}})
    } else if req["capability"] == json!("source") && req["method"] == json!("poll") {
        eprintln!("polling upstream..."); // diagnostics → stderr only
        json!({"result": {"items": [{
            "id": "rs-1",
            "title": "Hello from Rust",
            "labels": ["origin:rust"],
            "state": "open"
        }]}})
    } else if req["capability"] == json!("source")
        && (req["method"] == json!("acknowledge") || req["method"] == json!("write_result"))
    {
        json!({"result": {"ok": true}})
    } else {
        json!({"error": {"code": "unsupported_method",
                         "message": format!("unknown {}.{}", req["capability"], req["method"])}})
    };

    let mut response = json!({"protocol": 1, "request_id": request_id});
    response.as_object_mut().unwrap().extend(body.as_object().unwrap().clone());
    println!("{}", response); // exactly one JSON object on stdout
}
```

Build with `cargo build --release`, copy `target/release/apiary-plugin-rust-source`
next to a manifest, done.

### Writing an SDK for your language

If you maintain plugins in another language, wrap the boilerplate once. A
minimal SDK must:

1. **Read stdin to EOF** and decode exactly one request object; treat trailing
   data as an error.
2. **Verify `protocol == 1`**; on mismatch, respond with an
   `unsupported_protocol` error rather than crashing.
3. **Echo `request_id` verbatim** in the response.
4. **Guard stdout purity**: emit exactly one JSON object, nothing before or
   after; route all logging to stderr.
5. **Enforce the result/error dichotomy**: exactly one of the two, `error`
   always carrying non-empty `code` and `message`.
6. **Exit 0 on a delivered response**, non-zero only on transport failures.
7. Optionally, mirror the typed structs from `sdk/plugin/source.go` so plugin
   authors get the same guidance the Go SDK gives (`id` required, RFC3339
   timestamps, `key:value` labels).

Keep it stateless — the process serves one request and exits, so an SDK needs
no connection handling, retries, or lifecycle management.
