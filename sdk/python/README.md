# apiary-plugin (Python)

The official Python SDK for [Apiary](https://github.com/orlandoburli/apiary)
plugins. It handles the protocol envelope — the single-shot JSON exchange on
stdin/stdout — and mirrors the typed `source` structs, so a plugin is one
handler function.

Standard library only, on purpose: a plugin is a short-lived subprocess the
daemon spawns, and it should not drag a dependency tree into that path.

## Install

```bash
pip install ./sdk/python          # from a checkout
```

The package is **not published to PyPI**; install it from the repository (or
vendor the `apiary_plugin/` directory, which is two files).

## Use

```python
#!/usr/bin/env python3
from apiary_plugin import CAPABILITY_SOURCE, PluginError, Request, main
from apiary_plugin.source import (
    SOURCE_METHOD_ACKNOWLEDGE,
    SOURCE_METHOD_POLL,
    SOURCE_METHOD_WRITE_RESULT,
    SourceItem,
    SourceOKResult,
    SourcePollResult,
)


def handle(request: Request):
    if request.capability != CAPABILITY_SOURCE:
        raise PluginError("unsupported_capability", "expected capability source")
    if request.method == SOURCE_METHOD_POLL:
        return SourcePollResult(items=[
            SourceItem(id="py-1", title="Hello from Python", labels=["origin:python"], state="open"),
        ])
    if request.method in (SOURCE_METHOD_ACKNOWLEDGE, SOURCE_METHOD_WRITE_RESULT):
        return SourceOKResult()
    raise PluginError("unsupported_method", f"unknown method {request.method}")


main(handle)
```

Make the script executable, pair it with a manifest declaring
`"capabilities": ["source"]`, and install it like any other plugin.

| Symbol | Purpose |
|---|---|
| `main(handler)` | Serve one request on stdin/stdout and exit — all a plugin's `__main__` needs |
| `serve_one(handler, stdin=…, stdout=…)` | The engine behind `main`, with injectable streams — use it in tests |
| `Request` | The decoded envelope: `protocol`, `request_id`, `capability`, `method`, `config`, `payload` |
| `PluginError(code, message)` | Raise it to return an error response; both fields must be non-empty |
| `TransportError` | The stream itself was unusable — no response can be delivered, `main` exits 2 |
| `apiary_plugin.source` | Typed mirrors of `sdk/plugin/source.go`: `SourceItem`, `SourcePollRequest/Result`, `SourceAckRequest`, `SourceWriteResultRequest`, `SourceOKResult` |

Your handler returns the result value; anything with a `to_dict()` (every typed
mirror here) encodes itself. Diagnostics go to **stderr** — stdout carries the
one response object and nothing else.

## Versioning

The version tracks the **protocol**, not the Apiary release: `apiary-plugin`
1.x speaks protocol 1, and a future protocol bump gets a new major version.
See [Versioning and the protocol](../../docs/plugin-sdk.md#versioning-and-the-protocol).

## Tests and conformance

```bash
python3 -m unittest discover -s sdk/python/tests          # unit tests
make conformance                                          # the shared protocol corpus
```

The [conformance kit](../conformance/README.md) runs this SDK's example plugin
(`examples/source_file.py`) against the same golden corpus as every other SDK.
