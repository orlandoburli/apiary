"""Unit tests for the Python plugin SDK. Run with: python3 -m unittest discover sdk/python/tests"""

import io
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from apiary_plugin import PluginError, Request, TransportError, decode_request, serve_one  # noqa: E402
from apiary_plugin.source import (  # noqa: E402
    SourceAckRequest,
    SourceItem,
    SourceOKResult,
    SourcePollRequest,
    SourcePollResult,
    SourceWriteResultRequest,
)


def serve(raw: str, handler):
    out = io.StringIO()
    serve_one(handler, stdin=io.StringIO(raw), stdout=out)
    return json.loads(out.getvalue())


class TestEnvelope(unittest.TestCase):
    def test_echoes_request_and_encodes_result(self):
        response = serve(
            '{"protocol":1,"request_id":"req-1","capability":"source","method":"poll"}',
            lambda request: SourcePollResult(items=[SourceItem(id="a")]),
        )
        self.assertEqual(response["protocol"], 1)
        self.assertEqual(response["request_id"], "req-1")
        self.assertEqual(response["result"], {"items": [{"id": "a"}]})
        self.assertNotIn("error", response)

    def test_request_id_is_echoed_verbatim(self):
        weird = "id with spaces & ünïcode/42"
        response = serve(
            json.dumps({"protocol": 1, "request_id": weird, "capability": "source", "method": "acknowledge"}),
            lambda request: SourceOKResult(),
        )
        self.assertEqual(response["request_id"], weird)

    def test_protocol_mismatch_short_circuits_the_handler(self):
        called = []
        response = serve(
            '{"protocol":99,"request_id":"req-2","capability":"source","method":"poll"}',
            lambda request: called.append(request) or SourceOKResult(),
        )
        self.assertEqual(called, [])
        self.assertEqual(response["error"]["code"], "unsupported_protocol")
        self.assertNotIn("result", response)

    def test_plugin_error_becomes_an_error_response(self):
        def handler(request):
            raise PluginError("read_failed", "boom")

        response = serve('{"protocol":1,"request_id":"req-3","capability":"source","method":"poll"}', handler)
        self.assertEqual(response["error"], {"code": "read_failed", "message": "boom"})
        self.assertNotIn("result", response)

    def test_plugin_error_rejects_empty_code_or_message(self):
        with self.assertRaises(ValueError):
            PluginError("", "message")
        with self.assertRaises(ValueError):
            PluginError("code", "")

    def test_stdout_carries_exactly_one_json_object(self):
        out = io.StringIO()
        serve_one(
            lambda request: SourceOKResult(),
            stdin=io.StringIO('{"protocol":1,"request_id":"r","capability":"source","method":"acknowledge"}'),
            stdout=out,
        )
        raw = out.getvalue()
        self.assertTrue(raw.endswith("\n"))
        self.assertEqual(len(raw.strip().splitlines()), 1)


class TestDecodeRequest(unittest.TestCase):
    def test_trailing_data_is_a_transport_error(self):
        one = '{"protocol":1,"request_id":"a","capability":"source","method":"poll"}'
        with self.assertRaises(TransportError):
            decode_request(one + "\n" + one)

    def test_trailing_whitespace_is_fine(self):
        request = decode_request('{"protocol":1,"request_id":"a","capability":"source","method":"poll"}\n\n  \n')
        self.assertEqual(request.request_id, "a")

    def test_empty_stdin_is_a_transport_error(self):
        with self.assertRaises(TransportError):
            decode_request("   \n")

    def test_unknown_envelope_field_is_rejected(self):
        with self.assertRaises(TransportError):
            decode_request('{"protocol":1,"request_id":"a","capability":"source","method":"poll","nope":1}')

    def test_non_object_request_is_rejected(self):
        with self.assertRaises(TransportError):
            decode_request("[1,2,3]")


class TestSourceMirrors(unittest.TestCase):
    def test_item_omits_empty_optionals_but_never_the_id(self):
        self.assertEqual(SourceItem(id="x").to_dict(), {"id": "x"})

    def test_item_round_trips(self):
        raw = {
            "id": "conf-1",
            "number": "CONF-1",
            "title": "t",
            "description": "d",
            "labels": ["origin:test"],
            "type": "task",
            "priority": "low",
            "state": "open",
            "url": "https://example.invalid/1",
            "metadata": {"a": 1},
            "created_at": "2026-01-02T03:04:05Z",
            "updated_at": "2026-01-02T03:04:05Z",
        }
        self.assertEqual(SourceItem.from_dict(raw).to_dict(), raw)

    def test_empty_poll_result_still_carries_the_items_key(self):
        self.assertEqual(SourcePollResult().to_dict(), {"items": []})

    def test_poll_request_payload(self):
        payload = SourcePollRequest.from_dict({"since": "2026-01-01T00:00:00Z", "states": ["open"]})
        self.assertEqual(payload.since, "2026-01-01T00:00:00Z")
        self.assertEqual(payload.states, ["open"])
        self.assertEqual(payload.labels, [])
        self.assertEqual(SourcePollRequest.from_dict(None).since, "")

    def test_write_back_payloads(self):
        ack = SourceAckRequest.from_dict({"item": {"id": "a"}, "action": "dispatched"})
        self.assertEqual((ack.item.id, ack.action), ("a", "dispatched"))
        write = SourceWriteResultRequest.from_dict({"item": {"id": "a"}, "success": True, "output": "ok"})
        self.assertEqual((write.item.id, write.success, write.output, write.error), ("a", True, "ok", ""))

    def test_ok_result(self):
        self.assertEqual(SourceOKResult().to_dict(), {"ok": True})


class TestRequest(unittest.TestCase):
    def test_defaults(self):
        request = Request.from_dict({"protocol": 1, "request_id": "a"})
        self.assertEqual(request.config, {})
        self.assertIsNone(request.payload)
        self.assertEqual(request.capability, "")


if __name__ == "__main__":
    unittest.main()
