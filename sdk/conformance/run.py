#!/usr/bin/env python3
"""Apiary plugin protocol conformance runner.

Drives any plugin executable — in any language — as a subprocess over
stdin/stdout and checks it against the golden case corpus in ``cases/``. The
corpus encodes the seven rules from the "Writing an SDK for your language"
section of ``docs/plugin-sdk.md``:

  1. Read stdin to EOF, decode exactly one request; trailing data is an error
  2. Verify ``protocol == 1``; respond ``unsupported_protocol`` on mismatch
  3. Echo ``request_id`` verbatim
  4. Stdout purity: exactly one JSON object, all logging to stderr
  5. Result/error dichotomy, ``error`` carrying non-empty ``code``+``message``
  6. Exit 0 on a delivered response
  7. Typed SourceItem mirrors: stable ``id``, RFC3339 timestamps,
     ``key:value`` labels

Usage:
    run.py [options] -- <plugin command> [args...]

Options:
    --config JSON   merged into every request's `config` block, so a plugin
                    that needs e.g. {"path": "items.json"} can be exercised
                    with real items instead of an empty poll
    --case NAME     run only the named case (repeatable)
    --name LABEL    label for the report header
    --verbose       print the request/response of every case
    --json          emit a machine-readable report on stdout

Exit status is 0 when every `must` case passes. `should` cases that fail are
reported as warnings and do not affect the exit status.

Standard library only, on purpose: the kit must run wherever a plugin author
can run their plugin.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

CASES_DIR = Path(__file__).resolve().parent / "cases"

# RFC3339: date-time with a mandatory offset (Z or ±HH:MM).
RFC3339 = re.compile(
    r"^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$"
)

RESET, RED, GREEN, YELLOW, DIM = "\033[0m", "\033[31m", "\033[32m", "\033[33m", "\033[2m"


def colorize(text: str, color: str) -> str:
    if not sys.stdout.isatty() or os.environ.get("NO_COLOR"):
        return text
    return f"{color}{text}{RESET}"


class Outcome:
    """The verdict on one case."""

    def __init__(self, case: dict):
        self.case = case
        self.name = case["name"]
        self.level = case.get("level", "must")
        self.rules = case.get("rules", [])
        self.failures: list[str] = []
        self.warnings: list[str] = []
        self.stdout = ""
        self.stderr = ""
        self.exit_code: int | None = None

    def fail(self, message: str) -> None:
        # A `should` case can only ever warn.
        (self.failures if self.level == "must" else self.warnings).append(message)

    def warn(self, message: str) -> None:
        self.warnings.append(message)

    @property
    def passed(self) -> bool:
        return not self.failures


def load_cases(only: list[str]) -> list[dict]:
    cases = []
    for path in sorted(CASES_DIR.glob("*.json")):
        case = json.loads(path.read_text(encoding="utf-8"))
        case["_file"] = path.name
        if only and case["name"] not in only:
            continue
        cases.append(case)
    if only:
        found = {case["name"] for case in cases}
        for name in only:
            if name not in found:
                raise SystemExit(f"no such case: {name}")
    return cases


def build_stdin(case: dict, config: dict) -> str:
    if "stdin_raw" in case:
        return case["stdin_raw"]
    request = dict(case["request"])
    if config:
        merged = dict(request.get("config") or {})
        merged.update(config)
        request["config"] = merged
    return json.dumps(request) + case.get("stdin_suffix", "\n")


def parse_stdout(raw: str) -> tuple[list, str | None]:
    """Decode the JSON values on stdout.

    Returns (values, error). Stdout purity means exactly one value and no
    trailing bytes other than whitespace, so anything else surfaces here.
    """
    decoder = json.JSONDecoder()
    values, index = [], 0
    while index < len(raw):
        while index < len(raw) and raw[index] in " \t\r\n":
            index += 1
        if index >= len(raw):
            break
        try:
            value, index = decoder.raw_decode(raw, index)
        except ValueError as err:
            return values, f"stdout is not pure JSON at byte {index}: {err}"
        values.append(value)
    return values, None


def check_source_item(item, where: str, outcome: Outcome) -> None:
    if not isinstance(item, dict):
        outcome.fail(f"{where} is {type(item).__name__}, want a SourceItem object")
        return
    item_id = item.get("id")
    if not isinstance(item_id, str) or item_id == "":
        outcome.fail(f"{where}.id must be a non-empty string (the host drops items without one), got {item_id!r}")
    for field in ("created_at", "updated_at"):
        value = item.get(field)
        if value in (None, ""):
            continue  # empty means "now"
        if not isinstance(value, str) or not RFC3339.match(value):
            outcome.fail(f"{where}.{field} must be an RFC3339 timestamp, got {value!r}")
    labels = item.get("labels")
    if labels is not None:
        if not isinstance(labels, list):
            outcome.fail(f"{where}.labels must be an array, got {type(labels).__name__}")
        else:
            for i, label in enumerate(labels):
                if not isinstance(label, str):
                    outcome.fail(f"{where}.labels[{i}] must be a string, got {label!r}")
                elif ":" not in label:
                    outcome.warn(f"{where}.labels[{i}]={label!r} is not in `key:value` form")
    for field, want in (("number", str), ("title", str), ("description", str), ("type", str),
                        ("priority", str), ("state", str), ("url", str), ("metadata", dict)):
        value = item.get(field)
        if value is not None and not isinstance(value, want):
            outcome.fail(f"{where}.{field} must be {want.__name__}, got {type(value).__name__}")


def check_result_shape(shape: str, result, outcome: Outcome) -> None:
    if shape == "source_poll":
        if not isinstance(result, dict):
            outcome.fail(f"result must be a SourcePollResult object, got {type(result).__name__}")
            return
        items = result.get("items")
        if items is None:
            outcome.fail("result.items is missing (SourcePollResult always carries an items array)")
            return
        if not isinstance(items, list):
            outcome.fail(f"result.items must be an array, got {type(items).__name__}")
            return
        seen = set()
        for i, item in enumerate(items):
            check_source_item(item, f"result.items[{i}]", outcome)
            if isinstance(item, dict) and isinstance(item.get("id"), str):
                if item["id"] in seen:
                    outcome.fail(f"result.items[{i}].id={item['id']!r} is duplicated; ids are the host's dedup key")
                seen.add(item["id"])
    elif shape == "source_ok":
        if not isinstance(result, dict):
            outcome.fail(f"result must be an object, got {type(result).__name__}")
        elif result.get("ok") is not True:
            outcome.warn('result is not the conventional {"ok": true} for a write-back method')


def run_case(command: list[str], case: dict, config: dict, cwd: str | None) -> Outcome:
    outcome = Outcome(case)
    stdin = build_stdin(case, config)
    try:
        proc = subprocess.run(
            command,
            input=stdin.encode("utf-8"),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
            timeout=30,
        )
    except FileNotFoundError:
        outcome.fail(f"plugin executable not found: {command[0]}")
        return outcome
    except subprocess.TimeoutExpired:
        outcome.fail("plugin did not exit within 30s (the protocol is single-shot; serve one request and exit)")
        return outcome

    outcome.stdout = proc.stdout.decode("utf-8", "replace")
    outcome.stderr = proc.stderr.decode("utf-8", "replace")
    outcome.exit_code = proc.returncode
    expect = case["expect"]

    values, parse_error = parse_stdout(outcome.stdout)
    want_stdout = expect.get("stdout", "single_json_object")
    if parse_error:
        outcome.fail(parse_error)
    if want_stdout == "single_json_object" and len(values) != 1:
        outcome.fail(f"stdout must carry exactly one JSON object, found {len(values)}")
    if want_stdout == "at_most_one_json_object" and len(values) > 1:
        outcome.fail(f"stdout must carry at most one JSON object, found {len(values)}")

    response = values[0] if values else None
    if response is not None and not isinstance(response, dict):
        outcome.fail(f"the response must be a JSON object, got {type(response).__name__}")
        response = None

    want_exit = expect.get("exit_code", 0)
    if want_exit != "any" and proc.returncode != want_exit:
        # Rule 6 is about *delivered* responses: a plugin that emitted nothing
        # is failing the transport, which the outcome checks below catch.
        if response is not None or want_stdout == "single_json_object":
            outcome.fail(f"exit code {proc.returncode}, want {want_exit} (a delivered response exits 0)")

    if response is not None:
        if "protocol" in expect and response.get("protocol") != expect["protocol"]:
            outcome.fail(f"response.protocol is {response.get('protocol')!r}, want {expect['protocol']}")
        if expect.get("request_id") == "echo" and "request" in case:
            want_id = case["request"]["request_id"]
            if response.get("request_id") != want_id:
                outcome.fail(f"response.request_id is {response.get('request_id')!r}, want {want_id!r} echoed verbatim")

        has_result = "result" in response and response["result"] is not None
        has_error = "error" in response and response["error"] is not None
        if has_result and has_error:
            outcome.fail("response carries both result and error; exactly one is allowed")
        if has_error:
            error = response["error"]
            if not isinstance(error, dict):
                outcome.fail(f"response.error must be an object, got {type(error).__name__}")
            else:
                for field in ("code", "message"):
                    value = error.get(field)
                    if not isinstance(value, str) or value == "":
                        outcome.fail(f"response.error.{field} must be a non-empty string, got {value!r}")
                want_code = expect.get("error_code")
                if want_code and error.get("code") != want_code:
                    outcome.fail(f"response.error.code is {error.get('code')!r}, want {want_code!r}")
                preferred = expect.get("error_code_preferred")
                if preferred and error.get("code") != preferred:
                    outcome.warn(f"response.error.code is {error.get('code')!r}; {preferred!r} is the conventional code")

        want_outcome = expect.get("outcome", "either")
        if want_outcome == "result" and not has_result:
            outcome.fail("response must carry a result" + (f" (got error {response['error']!r})" if has_error else ""))
        if want_outcome == "error" and not has_error:
            outcome.fail("response must carry an error")
        if want_outcome == "no_result" and has_result:
            outcome.fail(f"response must not carry a success result, got {json.dumps(response.get('result'))[:200]}")
        if want_outcome == "either" and not has_result and not has_error:
            outcome.fail("response carries neither result nor error; exactly one is required")

        shape = expect.get("result_shape")
        if shape and has_result:
            check_result_shape(shape, response["result"], outcome)
    elif expect.get("outcome") in ("result", "error", "either"):
        outcome.fail("no JSON response on stdout")

    return outcome


def main() -> int:
    parser = argparse.ArgumentParser(add_help=True, description="Apiary plugin protocol conformance runner")
    parser.add_argument("--config", default="", help="JSON object merged into every request's config block")
    parser.add_argument("--case", action="append", default=[], help="run only this case (repeatable)")
    parser.add_argument("--name", default="", help="label for the report header")
    parser.add_argument("--cwd", default=None, help="working directory for the plugin subprocess")
    parser.add_argument("--verbose", action="store_true")
    parser.add_argument("--json", dest="as_json", action="store_true", help="emit a machine-readable report")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="-- <plugin command> [args...]")
    args = parser.parse_args()

    command = args.command[1:] if args.command and args.command[0] == "--" else args.command
    if not command:
        parser.error("a plugin command is required: run.py [options] -- ./my-plugin")

    config = json.loads(args.config) if args.config else {}
    if not isinstance(config, dict):
        parser.error("--config must be a JSON object")

    cases = load_cases(args.case)
    label = args.name or command[0]
    outcomes = [run_case(command, case, config, args.cwd) for case in cases]

    if args.as_json:
        print(json.dumps({
            "plugin": label,
            "command": command,
            "cases": [
                {
                    "name": o.name,
                    "level": o.level,
                    "rules": o.rules,
                    "passed": o.passed,
                    "failures": o.failures,
                    "warnings": o.warnings,
                    "exit_code": o.exit_code,
                }
                for o in outcomes
            ],
        }, indent=2))
        return 0 if all(o.passed for o in outcomes) else 1

    stream = sys.stdout
    print(f"\nconformance: {label}", file=stream)
    print(f"  command: {' '.join(command)}", file=stream)
    if config:
        print(f"  config:  {json.dumps(config)}", file=stream)
    print("", file=stream)

    for outcome in outcomes:
        rules = ",".join(str(rule) for rule in outcome.rules)
        if outcome.passed and not outcome.warnings:
            mark = colorize("PASS", GREEN)
        elif outcome.passed:
            mark = colorize("WARN", YELLOW)
        else:
            mark = colorize("FAIL", RED)
        suffix = "" if outcome.level == "must" else colorize("  [should]", DIM)
        print(f"  {mark}  {outcome.name:<24} {colorize('rules ' + rules, DIM)}{suffix}", file=stream)
        for failure in outcome.failures:
            print(f"          {colorize('× ' + failure, RED)}", file=stream)
        for warning in outcome.warnings:
            print(f"          {colorize('! ' + warning, YELLOW)}", file=stream)
        if args.verbose:
            print(f"          stdout: {outcome.stdout.strip()!r}", file=stream)
            print(f"          stderr: {outcome.stderr.strip()!r}", file=stream)
            print(f"          exit:   {outcome.exit_code}", file=stream)

    failed = [o for o in outcomes if not o.passed]
    warned = sum(len(o.warnings) for o in outcomes)
    print("", file=stream)
    if failed:
        print(colorize(f"  {len(outcomes) - len(failed)}/{len(outcomes)} cases passed, {len(failed)} failed", RED), file=stream)
        return 1
    print(colorize(f"  {len(outcomes)}/{len(outcomes)} cases passed", GREEN)
          + (colorize(f" ({warned} warning(s))", YELLOW) if warned else ""), file=stream)
    return 0


if __name__ == "__main__":
    sys.exit(main())
