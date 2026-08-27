"""Typed mirrors of the ``source`` capability wire contract.

A line-for-line counterpart of ``sdk/plugin/source.go``. Optional fields are
omitted from the encoded JSON, matching the Go structs' ``omitempty`` tags, so
a plugin written against either SDK puts the same bytes on the wire.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, List, Mapping, Optional

# Method names a source plugin switches on.
SOURCE_METHOD_POLL = "poll"
SOURCE_METHOD_ACKNOWLEDGE = "acknowledge"
SOURCE_METHOD_WRITE_RESULT = "write_result"


def _compact(values: dict) -> dict:
    """Drop empty optional fields — the `omitempty` of the Go structs."""
    return {key: value for key, value in values.items() if value not in (None, "", [], {})}


@dataclass
class SourceItem:
    """One unit of work surfaced by a source plugin.

    ``id`` is the source-native identifier **and the host's dedup key**: it must
    be stable for the lifetime of the item and unique per dispatch-worthy
    occurrence. Items without one are dropped by the host, so it is the only
    required field. Returning the same item on every poll while it stays
    relevant is the expected shape.

    ``labels`` drive workflow trigger matching and use ``key:value`` form.
    ``created_at`` / ``updated_at`` are RFC3339 strings; empty means "now".
    """

    id: str
    number: str = ""
    title: str = ""
    description: str = ""
    labels: List[str] = field(default_factory=list)
    type: str = ""
    priority: str = ""
    state: str = ""
    url: str = ""
    metadata: Mapping[str, Any] = field(default_factory=dict)
    created_at: str = ""
    updated_at: str = ""

    def to_dict(self) -> dict:
        encoded = _compact(
            {
                "number": self.number,
                "title": self.title,
                "description": self.description,
                "labels": list(self.labels),
                "type": self.type,
                "priority": self.priority,
                "state": self.state,
                "url": self.url,
                "metadata": dict(self.metadata),
                "created_at": self.created_at,
                "updated_at": self.updated_at,
            }
        )
        # id is never omitted: an item without one is not dispatchable.
        return {"id": self.id, **encoded}

    @classmethod
    def from_dict(cls, raw: Mapping[str, Any]) -> "SourceItem":
        return cls(
            id=raw.get("id", ""),
            number=raw.get("number", ""),
            title=raw.get("title", ""),
            description=raw.get("description", ""),
            labels=list(raw.get("labels") or []),
            type=raw.get("type", ""),
            priority=raw.get("priority", ""),
            state=raw.get("state", ""),
            url=raw.get("url", ""),
            metadata=dict(raw.get("metadata") or {}),
            created_at=raw.get("created_at", ""),
            updated_at=raw.get("updated_at", ""),
        )


@dataclass
class SourcePollRequest:
    """Payload of ``poll``.

    ``since`` is the previous poll time (RFC3339, empty on the first poll) and
    is informational: plugins that re-return ongoing items may ignore it.
    ``states`` / ``labels`` are the source's ``filters:`` from apiary.yaml,
    forwarded so the plugin can filter at the backend where possible.
    """

    since: str = ""
    states: List[str] = field(default_factory=list)
    labels: List[str] = field(default_factory=list)

    @classmethod
    def from_dict(cls, raw: Optional[Mapping[str, Any]]) -> "SourcePollRequest":
        raw = raw or {}
        return cls(
            since=raw.get("since", ""),
            states=list(raw.get("states") or []),
            labels=list(raw.get("labels") or []),
        )


@dataclass
class SourcePollResult:
    """Result of ``poll``: the plugin's current work items."""

    items: List[SourceItem] = field(default_factory=list)

    def to_dict(self) -> dict:
        # `items` is always present, empty array included — "no work right now"
        # is an answer, and dropping the key would look like a malformed result.
        return {"items": [item.to_dict() for item in self.items]}


@dataclass
class SourceAckRequest:
    """Payload of ``acknowledge``: the item and the host's ack action."""

    item: SourceItem
    action: str = ""

    @classmethod
    def from_dict(cls, raw: Optional[Mapping[str, Any]]) -> "SourceAckRequest":
        raw = raw or {}
        return cls(item=SourceItem.from_dict(raw.get("item") or {}), action=raw.get("action", ""))


@dataclass
class SourceWriteResultRequest:
    """Payload of ``write_result``: the run outcome for an item."""

    item: SourceItem
    success: bool = False
    output: str = ""
    error: str = ""

    @classmethod
    def from_dict(cls, raw: Optional[Mapping[str, Any]]) -> "SourceWriteResultRequest":
        raw = raw or {}
        return cls(
            item=SourceItem.from_dict(raw.get("item") or {}),
            success=bool(raw.get("success", False)),
            output=raw.get("output", ""),
            error=raw.get("error", ""),
        )


@dataclass
class SourceOKResult:
    """The conventional ``{"ok": true}`` answer to a write-back method."""

    ok: bool = True

    def to_dict(self) -> dict:
        return {"ok": self.ok}
