"""Apiary plugin SDK for Python.

Wraps Apiary's single-shot JSON plugin protocol (version 1) so a plugin is one
handler function::

    from apiary_plugin import PluginError, Request, main
    from apiary_plugin.source import SOURCE_METHOD_POLL, SourceItem, SourcePollResult

    def handle(request: Request):
        if request.method == SOURCE_METHOD_POLL:
            return SourcePollResult(items=[SourceItem(id="1", title="Hello")])
        raise PluginError("unsupported_method", f"unknown method {request.method}")

    main(handle)

The version tracks the **protocol**, not the Apiary release: 1.x speaks
protocol 1.
"""

from .protocol import (
    CAPABILITY_APPROVAL_PROVIDER,
    CAPABILITY_EVENT_EXPORTER,
    CAPABILITY_RUNNER,
    CAPABILITY_SECRET_PROVIDER,
    CAPABILITY_SOURCE,
    CAPABILITY_WORKFLOW_ACTION,
    PROTOCOL_VERSION,
    Handler,
    PluginError,
    Request,
    TransportError,
    decode_request,
    main,
    serve_one,
)

__version__ = "1.0.0"

__all__ = [
    "CAPABILITY_APPROVAL_PROVIDER",
    "CAPABILITY_EVENT_EXPORTER",
    "CAPABILITY_RUNNER",
    "CAPABILITY_SECRET_PROVIDER",
    "CAPABILITY_SOURCE",
    "CAPABILITY_WORKFLOW_ACTION",
    "PROTOCOL_VERSION",
    "Handler",
    "PluginError",
    "Request",
    "TransportError",
    "decode_request",
    "main",
    "serve_one",
    "__version__",
]
