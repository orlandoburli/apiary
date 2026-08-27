#!/usr/bin/env python3
"""Reference protocol-1 source plugin in Python.

Behaviourally identical to the Go `src/examples/plugins/source-file` plugin:
it polls Apiary work items from the JSON file named by `config.path`, so any
external process can drop items there and have Apiary dispatch workflows for
them. It is also the plugin the conformance kit runs to validate this SDK.
"""

import json
import os
import sys

# Importable in-tree without installing the package; `pip install apiary-plugin`
# makes this line unnecessary.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from apiary_plugin import CAPABILITY_SOURCE, PluginError, Request, main  # noqa: E402
from apiary_plugin.source import (  # noqa: E402
    SOURCE_METHOD_ACKNOWLEDGE,
    SOURCE_METHOD_POLL,
    SOURCE_METHOD_WRITE_RESULT,
    SourceItem,
    SourceOKResult,
    SourcePollResult,
)


def poll(request: Request) -> SourcePollResult:
    path = request.config.get("path")
    if not isinstance(path, str) or path == "":
        raise PluginError("invalid_config", "config.path is required")
    try:
        with open(path, "r", encoding="utf-8") as handle:
            raw = handle.read()
    except FileNotFoundError:
        return SourcePollResult(items=[])  # an absent file simply means no work yet
    except OSError as err:
        raise PluginError("read_failed", str(err)) from err
    try:
        items = json.loads(raw)
    except ValueError as err:
        raise PluginError("invalid_items", f"{path} must hold a JSON array of items: {err}") from err
    if not isinstance(items, list):
        raise PluginError("invalid_items", f"{path} must hold a JSON array of items")
    print(f"poll: {len(items)} item(s) from {path}", file=sys.stderr)  # diagnostics → stderr
    return SourcePollResult(items=[SourceItem.from_dict(item) for item in items])


def handle(request: Request):
    if request.capability != CAPABILITY_SOURCE:
        raise PluginError("unsupported_capability", "expected capability source")
    if request.method == SOURCE_METHOD_POLL:
        return poll(request)
    if request.method in (SOURCE_METHOD_ACKNOWLEDGE, SOURCE_METHOD_WRITE_RESULT):
        # Nothing to mark in a plain file; report success so host logs stay clean.
        return SourceOKResult(ok=True)
    raise PluginError("unsupported_method", f"unknown method {request.method}")


if __name__ == "__main__":
    main(handle)
