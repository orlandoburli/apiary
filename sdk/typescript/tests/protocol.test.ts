/**
 * Unit tests for the TypeScript plugin SDK.
 * Run with: npm test (builds, then `node --test dist/tests/protocol.test.js`).
 */

import assert from "node:assert/strict";
import test from "node:test";

import {
  Handler,
  PluginError,
  Request,
  TransportError,
  decodeRequest,
  serveOne,
} from "../src/index.js";
import {
  SourceAckRequest,
  SourceItem,
  SourceOKResult,
  SourcePollRequest,
  SourcePollResult,
  SourceWriteResultRequest,
} from "../src/index.js";

async function serve(raw: string, handler: Handler): Promise<any> {
  let out = "";
  await serveOne(handler, {
    input: raw,
    output: (text) => {
      out += text;
    },
  });
  return JSON.parse(out);
}

test("echoes the request and encodes the result", async () => {
  const response = await serve(
    '{"protocol":1,"request_id":"req-1","capability":"source","method":"poll"}',
    () => new SourcePollResult([new SourceItem({ id: "a" })]),
  );
  assert.equal(response.protocol, 1);
  assert.equal(response.request_id, "req-1");
  assert.deepEqual(response.result, { items: [{ id: "a" }] });
  assert.equal("error" in response, false);
});

test("echoes request_id verbatim", async () => {
  const weird = "id with spaces & ünïcode/42";
  const response = await serve(
    JSON.stringify({
      protocol: 1,
      request_id: weird,
      capability: "source",
      method: "acknowledge",
    }),
    () => new SourceOKResult(),
  );
  assert.equal(response.request_id, weird);
});

test("protocol mismatch short-circuits the handler", async () => {
  let called = false;
  const response = await serve(
    '{"protocol":99,"request_id":"req-2","capability":"source","method":"poll"}',
    () => {
      called = true;
      return new SourceOKResult();
    },
  );
  assert.equal(called, false);
  assert.equal(response.error.code, "unsupported_protocol");
  assert.equal("result" in response, false);
});

test("PluginError becomes an error response", async () => {
  const response = await serve(
    '{"protocol":1,"request_id":"req-3","capability":"source","method":"poll"}',
    () => {
      throw new PluginError("read_failed", "boom");
    },
  );
  assert.deepEqual(response.error, { code: "read_failed", message: "boom" });
  assert.equal("result" in response, false);
});

test("PluginError rejects an empty code or message", () => {
  assert.throws(() => new PluginError("", "message"));
  assert.throws(() => new PluginError("code", ""));
});

test("stdout carries exactly one JSON object", async () => {
  let raw = "";
  await serveOne(() => new SourceOKResult(), {
    input:
      '{"protocol":1,"request_id":"r","capability":"source","method":"acknowledge"}',
    output: (text) => {
      raw += text;
    },
  });
  assert.ok(raw.endsWith("\n"));
  assert.equal(raw.trim().split("\n").length, 1);
});

test("trailing data is a transport error", () => {
  const one =
    '{"protocol":1,"request_id":"a","capability":"source","method":"poll"}';
  assert.throws(() => decodeRequest(`${one}\n${one}`), TransportError);
});

test("trailing whitespace is fine", () => {
  const request = decodeRequest(
    '{"protocol":1,"request_id":"a","capability":"source","method":"poll"}\n\n  \n',
  );
  assert.equal(request.request_id, "a");
});

test("empty stdin is a transport error", () => {
  assert.throws(() => decodeRequest("   \n"), TransportError);
});

test("an unknown envelope field is rejected", () => {
  assert.throws(
    () =>
      decodeRequest(
        '{"protocol":1,"request_id":"a","capability":"source","method":"poll","nope":1}',
      ),
    TransportError,
  );
});

test("a non-object request is rejected", () => {
  assert.throws(() => decodeRequest("[1,2,3]"), TransportError);
});

test("an item omits empty optionals but never the id", () => {
  assert.deepEqual(new SourceItem({ id: "x" }).toJSON(), { id: "x" });
});

test("an item round-trips", () => {
  const raw = {
    id: "conf-1",
    number: "CONF-1",
    title: "t",
    description: "d",
    labels: ["origin:test"],
    type: "task",
    priority: "low",
    state: "open",
    url: "https://example.invalid/1",
    metadata: { a: 1 },
    created_at: "2026-01-02T03:04:05Z",
    updated_at: "2026-01-02T03:04:05Z",
  };
  assert.deepEqual(SourceItem.fromDict(raw).toJSON(), raw);
});

test("an empty poll result still carries the items key", () => {
  assert.deepEqual(new SourcePollResult().toJSON(), { items: [] });
});

test("poll request payload", () => {
  const payload = SourcePollRequest.fromDict({
    since: "2026-01-01T00:00:00Z",
    states: ["open"],
  });
  assert.equal(payload.since, "2026-01-01T00:00:00Z");
  assert.deepEqual(payload.states, ["open"]);
  assert.deepEqual(payload.labels, []);
  assert.equal(SourcePollRequest.fromDict(null).since, "");
});

test("write-back payloads", () => {
  const ack = SourceAckRequest.fromDict({
    item: { id: "a" },
    action: "dispatched",
  });
  assert.equal(ack.item.id, "a");
  assert.equal(ack.action, "dispatched");

  const write = SourceWriteResultRequest.fromDict({
    item: { id: "a" },
    success: true,
    output: "ok",
  });
  assert.equal(write.item.id, "a");
  assert.equal(write.success, true);
  assert.equal(write.output, "ok");
  assert.equal(write.error, "");
});

test("ok result", () => {
  assert.deepEqual(new SourceOKResult().toJSON(), { ok: true });
});

test("request defaults", () => {
  const request = Request.fromDict({ protocol: 1, request_id: "a" });
  assert.deepEqual(request.config, {});
  assert.equal(request.payload, null);
  assert.equal(request.capability, "");
});
