# Plugin protocol conformance kit

A language-agnostic test corpus for Apiary's plugin protocol (version 1).

Protocol 1 is small enough to implement by hand in any language, which is the
point — but "small" is not "obvious", and every implementation gets the same
handful of details wrong. This kit turns the seven contract rules from
[Writing an SDK for your language](../../docs/plugin-sdk.md#writing-an-sdk-for-your-language)
into golden request fixtures plus the expectations each response must meet,
and runs them against **any plugin executable** as a subprocess over
stdin/stdout. Go SDK, Python SDK, a Bash script, a Rust binary — same corpus,
same verdict.

## Run it

```bash
python3 sdk/conformance/run.py -- ./my-plugin
```

Against a plugin that needs configuration, pass the `config:` block that the
host would have supplied from `apiary.yaml`:

```bash
python3 sdk/conformance/run.py \
  --config '{"path":"sdk/conformance/fixtures/items.json"}' \
  -- ./my-plugin
```

| Option | Effect |
|---|---|
| `--config JSON` | merged into every request's `config` block |
| `--case NAME` | run one case (repeatable) |
| `--name LABEL` | label for the report header |
| `--cwd DIR` | working directory for the plugin subprocess |
| `--verbose` | dump each case's stdout/stderr/exit code |
| `--json` | machine-readable report |

Exit status is 0 when every `must` case passes. `should` cases are advisory:
they surface as warnings and never fail the run.

The runner needs nothing but `python3` — no pip install, no virtualenv. The
plugin under test needs nothing but the ability to be executed.

## Check everything this repo ships

```bash
make conformance
```

`check-examples.sh` runs the corpus against the Go SDK's example plugin, the
Python SDK's example plugin, the in-tree Bash and Node plugins, and the
hand-rolled Python/Rust/Bash examples **extracted from the docs** — so a
documented snippet that drifts out of conformance fails the build instead of
quietly misleading a reader. Missing toolchains (no Node, no Rust) are skipped
with a notice rather than failing.

## What the corpus covers

| Case | Rules | Checks |
|---|---|---|
| `poll-result-shape` | 1,3,4,5,6,7 | a well-formed `source.poll` returns exactly one JSON object, protocol 1, id echoed, one of result/error, exit 0; a result mirrors `SourcePollResult` (items array, non-empty stable `id`, RFC3339 timestamps, string labels, no duplicate ids) |
| `protocol-mismatch` | 2 | protocol 99 is refused with error code exactly `unsupported_protocol` — not a crash, not a result |
| `protocol-zero` | 2 | protocol 0 (the zero value of a dropped field) is refused the same way |
| `request-id-verbatim` | 3 | an id with spaces, punctuation and non-ASCII comes back byte for byte |
| `acknowledge-ok` | 4,5,6 | `source.acknowledge` answers with a result, conventionally `{"ok": true}` |
| `write-result-ok` | 4,5,6 | same for `source.write_result` |
| `unknown-method` | 5,6 | an unknown method is a reported error with non-empty `code`+`message`, still exit 0 |
| `unknown-capability` *(should)* | 5,6 | a capability the plugin does not implement should be refused, not served by whichever method handler happens to match |
| `trailing-data` | 1 | a second object glued onto the stream must never produce a success result |
| `empty-stdin` | 1,4 | an empty stream carries no request, so no result may be invented |

Stdout purity (rule 4) is enforced on **every** case, not just its own: each
response is parsed strictly, and anything besides exactly one JSON object —
a stray `print`, a banner, a second line — fails the case.

## Add a case

Drop a JSON file in `cases/`. The runner reads them in filename order.

```json
{
  "name": "my-case",
  "rules": [5],
  "level": "must",
  "description": "Why this rule matters, in a sentence.",
  "request": { "protocol": 1, "request_id": "conf-my-case", "capability": "source", "method": "poll" },
  "expect": {
    "exit_code": 0,
    "stdout": "single_json_object",
    "protocol": 1,
    "request_id": "echo",
    "outcome": "either",
    "result_shape": "source_poll"
  }
}
```

| Field | Values |
|---|---|
| `level` | `must` (failures fail the run) or `should` (failures warn) |
| `stdin_raw` | send this exact string instead of an encoded `request` |
| `stdin_suffix` | appended after the encoded request — for framing cases |
| `expect.exit_code` | a number, or `"any"` |
| `expect.stdout` | `single_json_object` or `at_most_one_json_object` |
| `expect.outcome` | `result`, `error`, `either`, `no_result` |
| `expect.error_code` | required exact code |
| `expect.error_code_preferred` | conventional code — a mismatch only warns |
| `expect.result_shape` | `source_poll` or `source_ok` |

Keep new cases anchored to a numbered rule. The corpus tests the **protocol**,
never a plugin's business logic: any case that only one implementation could
pass does not belong here.
