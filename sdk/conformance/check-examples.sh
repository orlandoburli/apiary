#!/usr/bin/env bash
# Run the conformance corpus against every plugin implementation this
# repository ships: the Go SDK's example plugins, the Python SDK's example, the
# in-tree Bash and Node plugins, and the hand-rolled examples embedded in the
# docs. One command, one verdict — `make conformance` calls this.
#
# Toolchains that are missing are skipped with a notice rather than failing the
# run, so a contributor without Node or Rust still gets a useful result; CI has
# all of them.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER="$REPO/sdk/conformance/run.py"
FIXTURE="$REPO/sdk/conformance/fixtures/items.json"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PYTHON="${PYTHON:-python3}"
FILE_CONFIG="{\"path\":\"$FIXTURE\"}"
failures=()
skipped=()

check() { # check <label> <config> <command...>
  local label="$1" config="$2"
  shift 2
  if ! "$PYTHON" "$RUNNER" --name "$label" --config "$config" -- "$@"; then
    failures+=("$label")
  fi
}

skip() {
  skipped+=("$1")
  echo ""
  echo "  SKIP  $1 ($2)"
}

# ── Go SDK: the reference implementation and its example plugin ───────────────
if command -v go >/dev/null 2>&1; then
  if (cd "$REPO/src" && go build -o "$WORK/source-file" ./examples/plugins/source-file); then
    check "go source-file (Go SDK)" "$FILE_CONFIG" "$WORK/source-file"
  else
    failures+=("go source-file (build)")
  fi
else
  skip "go source-file (Go SDK)" "go not installed"
fi

# ── Python SDK ───────────────────────────────────────────────────────────────
check "python source_file (Python SDK)" "$FILE_CONFIG" "$PYTHON" "$REPO/sdk/python/examples/source_file.py"

# ── In-tree hand-rolled example plugins ──────────────────────────────────────
if command -v jq >/dev/null 2>&1; then
  check "in-tree source-bash" "$FILE_CONFIG" "$REPO/src/examples/plugins/source-bash/apiary-plugin-source-bash"
else
  skip "in-tree source-bash" "jq not installed"
fi

NODE_PLUGIN="$REPO/src/examples/plugins/source-node/apiary-plugin-source-node"
if [[ -x "$NODE_PLUGIN" ]]; then
  check "in-tree source-node" "$FILE_CONFIG" "$NODE_PLUGIN"
else
  skip "in-tree source-node" "not built — run 'npm install && npm run build' in src/examples/plugins/source-node"
fi

# ── The examples embedded in the docs ────────────────────────────────────────
if ! "$PYTHON" "$REPO/sdk/conformance/extract_doc_examples.py" "$WORK/docs" >/dev/null; then
  failures+=("docs example extraction")
else
  check "docs Python (plugins.md)" "{}" "$PYTHON" "$WORK/docs/py-doc.py"
  if command -v jq >/dev/null 2>&1; then
    check "docs Bash (plugin-sdk.md)" "{}" "bash" "$WORK/docs/bash-doc.sh"
  else
    skip "docs Bash (plugin-sdk.md)" "jq not installed"
  fi
  if command -v cargo >/dev/null 2>&1; then
    if (cd "$WORK/docs/rust" && cargo build --release --quiet 2>&1 | tail -3); then
      check "docs Rust (plugin-sdk.md)" "{}" "$WORK/docs/rust/target/release/apiary-plugin-rust-source"
    else
      failures+=("docs Rust (build)")
    fi
  else
    skip "docs Rust (plugin-sdk.md)" "cargo not installed"
  fi
fi

echo ""
if ((${#skipped[@]})); then
  echo "skipped: ${skipped[*]}"
fi
if ((${#failures[@]})); then
  echo "CONFORMANCE FAILED: ${failures[*]}"
  exit 1
fi
echo "CONFORMANCE OK — every checked plugin conforms to protocol 1"
