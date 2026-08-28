# Plugin SDK

Plugins talk to Apiary over a deliberately small JSON protocol (see
[Protocol version 1](plugins.md#protocol-version-1)), so a plugin can be
written in **any language**. Official SDKs handle the protocol envelope for you
in **Go** and **Python**; for everything else this page specifies exactly what
your code — or an SDK you build for your language — must do, and the
[conformance kit](#the-conformance-kit) checks that it does.

## Versioning and the protocol

**Every SDK version tracks the protocol, not the Apiary release.** The daemon
and the SDKs move on separate schedules and are versioned separately; what
binds them is the wire protocol a given SDK speaks. Any SDK whose protocol
version matches the daemon's is compatible — so pin whichever versions you
like.

| Artifact | Version scheme | Current |
|---|---|---|
| Go SDK (`github.com/orlandoburli/apiary/sdk`) | tag `sdk/vX.Y.Z` | `sdk/v1.x` → protocol 1 |
| Python SDK (`apiary-plugin`, in `sdk/python`) | `X.Y.Z` | `1.x` → protocol 1 |
| Apiary daemon | tag `vX.Y.Z` | see [releases](https://github.com/orlandoburli/apiary/releases) |

An SDK stays on major version 1 for as long as protocol 1 is what it speaks;
a future protocol bump gets its own SDK major version (for Go, `sdk/v2.x` with
import path `github.com/orlandoburli/apiary/sdk/v2`; for Python,
`apiary-plugin` 2.x). Within a protocol, SDK patch and minor releases are
additive and safe to upgrade. Any SDK you write for another language should
follow the same rule.

## Go SDK

Import path:

```go
import pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
```

Add it with:

```bash
go get github.com/orlandoburli/apiary/sdk@latest
```

The SDK is its **own Go module** (`github.com/orlandoburli/apiary/sdk`, living
in `sdk/` at the repository root), separate from the daemon module. Depending
on it pulls in **nothing but the standard library** — none of the daemon's
dependency graph.

The tag `sdk/vX.Y.Z` releases it, independently of the daemon's `vX.Y.Z` —
`sdk/v1.0.0` is fetched as `go get github.com/orlandoburli/apiary/sdk@v1.0.0`.
The constant `pluginsdk.ProtocolVersion` is the protocol it speaks; see
[Versioning and the protocol](#versioning-and-the-protocol).

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

## Python SDK

Import path:

```python
from apiary_plugin import CAPABILITY_SOURCE, PluginError, Request, main
from apiary_plugin.source import SOURCE_METHOD_POLL, SourceItem, SourceOKResult, SourcePollResult
```

Install it from a checkout — the package lives in `sdk/python` and is **not
published to PyPI**:

```bash
pip install ./sdk/python
```

Like the Go SDK it depends on **nothing but the standard library**, and its
version tracks the protocol (`apiary-plugin` 1.x speaks protocol 1) — see
[Versioning and the protocol](#versioning-and-the-protocol).

### Entry points

| Symbol | Purpose |
|---|---|
| `main(handler)` | Call from `__main__`: serves one request on stdin/stdout, reports transport failures on stderr, exits 2 on them |
| `serve_one(handler, stdin=…, stdout=…)` | The engine behind `main`, with injectable streams — use it in tests |
| `Request` | The decoded envelope: `protocol`, `request_id`, `capability`, `method`, `config`, `payload` |
| `PluginError(code, message)` | **Raise** it to return an error response; both fields must be non-empty (the constructor enforces it) |
| `TransportError` | The stream itself was unusable, so no response can be delivered — distinct from a *delivered* error |
| `apiary_plugin.source` | Typed mirrors of `sdk/plugin/source.go`: `SourceItem`, `SourcePollRequest`/`SourcePollResult`, `SourceAckRequest`, `SourceWriteResultRequest`, `SourceOKResult`, and the method-name constants |

Where the Go SDK returns `(result, *ResponseError)`, the Python SDK returns the
result and raises `PluginError` — the same dichotomy in idiomatic Python.
Anything with a `to_dict()` (every typed mirror) encodes itself, and empty
optional fields are dropped exactly as the Go structs' `omitempty` drops them,
so both SDKs put the same bytes on the wire.

### A complete source plugin in Python

This is the bundled example (`sdk/python/examples/source_file.py`), trimmed to
its essence — the same behaviour as the Go `source-file` plugin:

```python
#!/usr/bin/env python3
import json

from apiary_plugin import CAPABILITY_SOURCE, PluginError, Request, main
from apiary_plugin.source import (
    SOURCE_METHOD_ACKNOWLEDGE, SOURCE_METHOD_POLL, SOURCE_METHOD_WRITE_RESULT,
    SourceItem, SourceOKResult, SourcePollResult,
)


def handle(request: Request):
    if request.capability != CAPABILITY_SOURCE:
        raise PluginError("unsupported_capability", "expected capability source")
    if request.method == SOURCE_METHOD_POLL:
        path = request.config.get("path")
        if not isinstance(path, str) or path == "":
            raise PluginError("invalid_config", "config.path is required")
        try:
            with open(path, encoding="utf-8") as handle_:
                items = json.load(handle_)
        except FileNotFoundError:
            return SourcePollResult(items=[])          # no file yet means no work yet
        except (OSError, ValueError) as err:
            raise PluginError("read_failed", str(err)) from err
        return SourcePollResult(items=[SourceItem.from_dict(item) for item in items])
    if request.method in (SOURCE_METHOD_ACKNOWLEDGE, SOURCE_METHOD_WRITE_RESULT):
        return SourceOKResult()
    raise PluginError("unsupported_method", f"unknown method {request.method}")


main(handle)
```

Make it executable, pair it with a manifest declaring
`"capabilities": ["source"]`, and [install it](plugins.md#installing-a-plugin)
like any other plugin.

## Other languages

TypeScript/Node and Rust SDKs are still open (tracked in
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
protocol=$(jq -r '.protocol' <<<"$req")
method=$(jq -r '.method' <<<"$req")

respond() { jq -nc --arg id "$request_id" "{protocol: 1, request_id: \$id} + $1"; }

if [[ "$protocol" != "1" ]]; then                # refuse, don't serve, on mismatch
  respond '{error: {code: "unsupported_protocol", message: "expected protocol 1"}}'
  exit 0
fi

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
go to stderr (`>&2`). Trimmed for the page, this one skips the `capability`
check; a plugin that may be asked for more than one capability should switch on
it too, the way `src/examples/plugins/source-bash` does — a complete,
installable Bash plugin with config handling, error responses and file-backed
items.

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

## The conformance kit

You do not have to take those seven rules on trust, and you should not have to
discover a violation from a daemon log. `sdk/conformance/` is a
**language-agnostic golden corpus**: JSON request fixtures plus the
expectations each response must meet, one case per rule, driven against any
plugin executable as a subprocess over stdin/stdout. The Go SDK, the Python
SDK, a Bash script and a Rust binary are all validated by the same corpus.

Point it at your plugin:

```bash
python3 sdk/conformance/run.py -- ./my-plugin
```

If your plugin needs configuration, pass the `config:` block the host would
have supplied from `apiary.yaml`:

```bash
python3 sdk/conformance/run.py \
  --config '{"path":"sdk/conformance/fixtures/items.json"}' \
  -- ./my-plugin
```

```text
  PASS  poll-result-shape        rules 1,3,4,5,6,7
  PASS  protocol-mismatch        rules 2
  …
  10/10 cases passed
```

The runner needs nothing but `python3` — no pip install, no virtualenv — and
your plugin needs nothing but the ability to be executed, so the kit is the
same amount of work in every ecosystem. Each case names the rules it enforces,
so a failure points at the paragraph above that it violates; cases marked
`[should]` are advisory and warn instead of failing. `make conformance` runs
the whole corpus against every plugin this repository ships, **including the
Python, Rust and Bash examples extracted from these docs** — a documented
snippet that drifts out of conformance fails CI rather than quietly misleading
a reader.

For the full case list and how to add one, see
[`sdk/conformance/README.md`](https://github.com/orlandoburli/apiary/blob/main/sdk/conformance/README.md).

### If you are writing a new-language SDK

1. Write the thinnest possible envelope wrapper and one example plugin that
   answers `poll`, `acknowledge` and `write_result` — the Python SDK's
   `sdk/python/examples/source_file.py` is the shape to copy.
2. Run the kit against that example until it is 10/10. Every rule has at least
   one case, so a green run is a real statement about the implementation, not
   a smoke test.
3. Mirror the typed source structs (rule 7). The corpus checks the wire shape
   your mirrors produce: stable non-empty `id`, RFC3339 timestamps, string
   `key:value` labels, no duplicate ids in one poll.
4. Version it against the **protocol**, per
   [Versioning and the protocol](#versioning-and-the-protocol).
5. Add it to `sdk/conformance/check-examples.sh` so it is checked on every
   change to the protocol, the docs or the SDKs.

## Publishing to the registry

The [registry](plugin-directory.md) is how operators find and install your
plugin from the command line. It stores metadata and digests only: your
artifacts stay on your own release infrastructure, and nothing about the listing
gives Apiary a copy of your code.

### 1. Publish release artifacts

One archive (`.tar.gz` or `.zip`) per platform, containing the executable and
its `apiary-plugin.json`. The manifest may sit at the archive root or inside a
single top-level directory. Anything that is not a plain file or directory —
symlinks, hardlinks, devices — is refused at install time, so do not ship them.

Releases are immutable: publish `1.0.1`, never a re-cut `1.0.0`.

### 2. Compute both digests

Every artifact declares two: the archive as you publish it, and the executable
*inside* it, after unpacking.

```bash
shasum -a 256 myplugin_1.0.0_linux_amd64.tar.gz              # archive_sha256
tar -xzf myplugin_1.0.0_linux_amd64.tar.gz -C /tmp/unpacked
shasum -a 256 /tmp/unpacked/myplugin                          # executable_sha256
```

The second one becomes the `checksum` pin in the installed manifest when your
own manifest carries none — which is why it comes from the registry rather than
from your archive. If you do pin a `checksum` yourself, it must be the
executable you ship, or the install aborts.

### 3. Open a pull request

Add `registry/plugins/<your-plugin-id>.yaml` to the
[apiary repository](https://github.com/orlandoburli/apiary). The filename must
be the plugin id.

```yaml
schema_version: 1
id: com.example.nagios
summary: One sentence on what it does.
capabilities: [source]
repository: https://github.com/example/apiary-nagios   # must be publicly readable
license: MIT

# Optional: the config registry CI runs the conformance kit with. Without it the
# release publishes as "conformance not run" — an honest absence rather than an
# unearned pass.
conformance_config:
  api_url: https://example.invalid/api

releases:
  - version: 1.0.0
    apiary: ">= 0.13.0-0"    # the same constraint as your manifest's
    protocol: 1
    artifacts:
      - os: linux            # GOOS/GOARCH spelling
        arch: amd64
        url: https://github.com/example/apiary-nagios/releases/download/v1.0.0/…tar.gz
        archive_sha256: "…"
        executable_sha256: "…"
```

### What CI checks

Nothing in the listing is taken on trust. For every artifact, on every pull
request: the entry is validated, the artifact is downloaded and its
`archive_sha256` re-derived, it is unpacked and the `apiary-plugin.json` inside
is cross-checked against your entry (id, version, protocol, `apiary` constraint,
capabilities), `executable_sha256` is re-derived from the executable the
manifest names, and — if you declared a `conformance_config` — the
[conformance kit](#the-conformance-kit) runs against your published binary.

A conformance failure does **not** block the listing. The verdict is published
instead, and `apiary plugins info` shows it: the registry describes plugins, it
does not certify them. You can run the same check locally with
`make registry-check`.

### Withdrawing a release

Mark it, never delete it — the index has to stay honest about what was once
resolvable:

```yaml
  - version: 1.0.0
    yanked: true
    yanked_reason: corrupt linux/amd64 archive; use 1.0.1
```

Resolution skips yanked releases and says why when nothing else qualifies.
Operators who already installed one are unaffected until they upgrade.
