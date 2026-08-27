"""Protocol envelope handling for Apiary plugins.

Mirrors ``sdk/plugin/protocol.go``: one request in on stdin, one response out
on stdout, then the process exits. Standard library only.
"""

from __future__ import annotations

import json
import sys
from dataclasses import dataclass, field
from typing import Any, Callable, Mapping, Optional

PROTOCOL_VERSION = 1

# Capability names. `source` and `event_exporter` are live; the rest reserve
# the vocabulary for capabilities the host does not bridge yet.
CAPABILITY_SOURCE = "source"
CAPABILITY_RUNNER = "runner"
CAPABILITY_WORKFLOW_ACTION = "workflow_action"
CAPABILITY_APPROVAL_PROVIDER = "approval_provider"
CAPABILITY_SECRET_PROVIDER = "secret_provider"
CAPABILITY_EVENT_EXPORTER = "event_exporter"

_ENVELOPE_FIELDS = frozenset({"protocol", "request_id", "capability", "method", "config", "payload"})


class TransportError(Exception):
    """The stream itself was unusable, so no response can be delivered.

    Raised for a malformed, absent or over-long request. ``main`` reports it on
    stderr and exits non-zero — deliberately distinct from ``PluginError``,
    which is a *delivered* answer.
    """


class PluginError(Exception):
    """A failure the plugin reports to the host as a well-formed error response.

    ``code`` is machine-readable (``"read_failed"``, ``"unsupported_method"``),
    ``message`` is for humans. Both must be non-empty — the host logs them, and
    an empty code is indistinguishable from success in an alert.
    """

    def __init__(self, code: str, message: str):
        if not code:
            raise ValueError("PluginError code must be non-empty")
        if not message:
            raise ValueError("PluginError message must be non-empty")
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message

    def to_dict(self) -> dict:
        return {"code": self.code, "message": self.message}


@dataclass
class Request:
    """One decoded request. ``protocol`` is always 1 by the time a handler runs."""

    protocol: int = 0
    request_id: str = ""
    capability: str = ""
    method: str = ""
    config: Mapping[str, Any] = field(default_factory=dict)
    payload: Any = None

    @classmethod
    def from_dict(cls, raw: Mapping[str, Any]) -> "Request":
        unknown = set(raw) - _ENVELOPE_FIELDS
        if unknown:
            # The Go SDK decodes with DisallowUnknownFields; matching that here
            # keeps a typo in a hand-written request from being silently eaten.
            raise TransportError(f"unknown envelope field(s): {', '.join(sorted(unknown))}")
        return cls(
            protocol=raw.get("protocol", 0),
            request_id=raw.get("request_id", ""),
            capability=raw.get("capability", ""),
            method=raw.get("method", ""),
            config=raw.get("config") or {},
            payload=raw.get("payload"),
        )


# A handler returns the result value for a successful response, or raises
# PluginError to return an error response. Anything the JSON encoder accepts
# works as a result; the typed classes in `apiary_plugin.source` encode
# themselves via `to_dict`.
Handler = Callable[[Request], Any]


def encode_result(value: Any) -> Any:
    """Encode a handler's return value, honouring `to_dict` on typed mirrors."""
    if value is None:
        return None
    to_dict = getattr(value, "to_dict", None)
    if callable(to_dict):
        return to_dict()
    if isinstance(value, (list, tuple)):
        return [encode_result(item) for item in value]
    return value


def decode_request(raw: str) -> Request:
    """Decode exactly one request object; trailing data is a transport error."""
    decoder = json.JSONDecoder()
    stripped = raw.strip()
    if not stripped:
        raise TransportError("no request on stdin")
    try:
        value, end = decoder.raw_decode(stripped)
    except ValueError as err:
        raise TransportError(f"decode request: {err}") from err
    if stripped[end:].strip():
        raise TransportError("unexpected trailing data after the request; the protocol is single-shot")
    if not isinstance(value, dict):
        raise TransportError(f"request must be a JSON object, got {type(value).__name__}")
    return Request.from_dict(value)


def serve_one(handler: Handler, stdin=None, stdout=None) -> None:
    """Read one request, invoke ``handler``, write one response.

    Single-shot on purpose: the host starts a fresh process per invocation, so
    there is no loop, no state and no lifecycle to manage.
    """
    if handler is None:
        raise TransportError("plugin handler is required")
    source = sys.stdin if stdin is None else stdin
    sink = sys.stdout if stdout is None else stdout

    request = decode_request(source.read())
    response: dict = {"protocol": PROTOCOL_VERSION, "request_id": request.request_id}
    if request.protocol != PROTOCOL_VERSION:
        response["error"] = {
            "code": "unsupported_protocol",
            "message": f"protocol {request.protocol} is unsupported; expected {PROTOCOL_VERSION}",
        }
    else:
        try:
            result = encode_result(handler(request))
            if result is not None:
                # Mirrors the Go SDK's `omitempty`: a nil result is left out of
                # the envelope rather than serialised as a null "answer".
                response["result"] = result
        except PluginError as err:
            response["error"] = err.to_dict()
    # Exactly one JSON object, nothing before or after it. Everything a plugin
    # wants to say to a human belongs on stderr.
    sink.write(json.dumps(response) + "\n")
    sink.flush()


def main(handler: Handler) -> None:
    """Entry point for a plugin executable: serve one request and exit.

    Exits 0 once a response is on stdout — including an error response, which
    is a delivered answer — and 2 when the transport failed and there is
    nothing to deliver.
    """
    try:
        serve_one(handler)
    except TransportError as err:
        print(err, file=sys.stderr)
        raise SystemExit(2)
