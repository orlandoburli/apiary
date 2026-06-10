#!/usr/bin/env bash
# cursor-cost-setup.sh — wire up settings.cursor_cost credentials.
#
# The Cursor CLI keeps a long-lived session token on the machine where it is
# logged in. This script extracts it, discovers team_id/user_id, verifies the
# credentials live against the dashboard usage API, and writes
# CURSOR_SESSION_TOKEN into a .env file — locally or across two machines.
#
# Same machine (Cursor CLI and apiary daemon together):
#   scripts/cursor-cost-setup.sh /path/to/daemon/.env
#
# Different machines:
#   # 1. on the machine where the Cursor CLI is logged in:
#   scripts/cursor-cost-setup.sh --export cursor-creds.env
#   # 2. copy the file over (scp cursor-creds.env daemon-host:), then there:
#   scripts/cursor-cost-setup.sh --import cursor-creds.env /path/to/daemon/.env
#   #    (and delete cursor-creds.env afterwards)
#
# Token sources probed, in order: macOS keychain (cursor-access-token),
# Linux secret-service (secret-tool), the CLI's auth.json
# ($CURSOR_CONFIG_DIR | ~/.cursor | ~/.config/cursor), and finally the
# Cursor IDE's state.vscdb. Requires python3; verification needs network
# access to cursor.com.
set -euo pipefail

usage() {
  sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

MODE="local"
EXPORT_FILE=""
IMPORT_FILE=""
ENV_FILE=""
case "${1:-}" in
  --export) MODE="export"; EXPORT_FILE="${2:-}" ;;
  --import) MODE="import"; IMPORT_FILE="${2:-}"; ENV_FILE="${3:-}"
            [ -n "$IMPORT_FILE" ] || usage ;;
  --help|-h) usage ;;
  *) ENV_FILE="${1:-}" ;;
esac

# ── Token extraction (export/local modes) ────────────────────────────────────
find_config_file() { # $1 = filename; echoes the first existing path
  for dir in "${CURSOR_CONFIG_DIR:-}" "$HOME/.cursor" "${XDG_CONFIG_HOME:-$HOME/.config}/cursor"; do
    [ -n "$dir" ] && [ -f "$dir/$1" ] && { echo "$dir/$1"; return 0; }
  done
  return 1
}

extract_token() {
  TOKEN=""
  SOURCE=""
  # 1. macOS keychain — where the CLI login lives on Macs.
  if command -v security >/dev/null 2>&1; then
    TOKEN=$(security find-generic-password -s cursor-access-token -w 2>/dev/null || true)
    [ -n "$TOKEN" ] && SOURCE="macOS keychain (cursor-access-token)"
  fi
  # 2. Linux secret-service — same logical name via libsecret.
  if [ -z "$TOKEN" ] && command -v secret-tool >/dev/null 2>&1; then
    TOKEN=$(secret-tool lookup service cursor-access-token 2>/dev/null || true)
    [ -n "$TOKEN" ] && SOURCE="secret-service (cursor-access-token)"
  fi
  # 3. The CLI's plain-file fallback (no secret service available).
  if [ -z "$TOKEN" ]; then
    if AUTH_JSON=$(find_config_file auth.json); then
      TOKEN=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1])).get('accessToken',''))" "$AUTH_JSON" 2>/dev/null || true)
      [ -n "$TOKEN" ] && SOURCE="$AUTH_JSON"
    fi
  fi
  # 4. Cursor IDE state — a different login than the CLI's is possible; warn.
  if [ -z "$TOKEN" ]; then
    for DB in "$HOME/Library/Application Support/Cursor/User/globalStorage/state.vscdb" \
              "${XDG_CONFIG_HOME:-$HOME/.config}/Cursor/User/globalStorage/state.vscdb"; do
      if [ -f "$DB" ] && command -v sqlite3 >/dev/null 2>&1; then
        TOKEN=$(sqlite3 "$DB" "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'" 2>/dev/null | tr -d '"' || true)
        [ -n "$TOKEN" ] && SOURCE="Cursor IDE state.vscdb — NOTE: the IDE login, which may differ from the CLI's account" && break
      fi
    done
  fi
  if [ -z "$TOKEN" ]; then
    echo "error: no Cursor session token found (is the Cursor CLI or IDE logged in on this machine?)" >&2
    exit 1
  fi
}

build_credentials() { # sets COOKIE TEAM_ID USER_ID EMAIL from TOKEN + cli-config.json
  local cli_config
  cli_config=$(find_config_file cli-config.json) || cli_config=""
  read -r COOKIE TEAM_ID USER_ID EMAIL <<EOF2
$(TOKEN="$TOKEN" CLI_CONFIG="$cli_config" python3 <<'PY'
import base64, json, os

tok = os.environ["TOKEN"].strip()
auth = {}
cfg = os.environ.get("CLI_CONFIG")
if cfg:
    try:
        auth = json.load(open(cfg)).get("authInfo") or {}
    except Exception:
        pass

uid = (auth.get("authId") or "").split("|")[-1]
if not uid and tok.count(".") == 2:
    p = tok.split(".")[1]
    p += "=" * (-len(p) % 4)
    uid = json.loads(base64.urlsafe_b64decode(p)).get("sub", "").split("|")[-1]
if not uid:
    raise SystemExit("error: could not determine the account user id")

print(f"{uid}%3A%3A{tok}", auth.get("teamId", 0), auth.get("userId", 0),
      auth.get("email", "unknown"))
PY
)
EOF2
}

# ── Live verification against the dashboard API ──────────────────────────────
verify_credentials() {
  COOKIE="$COOKIE" TEAM_ID="$TEAM_ID" USER_ID="$USER_ID" python3 <<'PY'
import json, os, time, urllib.request

cookie, team, user = os.environ["COOKIE"], int(os.environ["TEAM_ID"]), int(os.environ["USER_ID"])
now = int(time.time() * 1000)
body = {"teamId": team, "startDate": str(now - 7 * 86400000), "endDate": str(now),
        "page": 1, "pageSize": 1}
if user:
    body["userId"] = user
req = urllib.request.Request(
    "https://cursor.com/api/dashboard/get-filtered-usage-events",
    data=json.dumps(body).encode(), method="POST")
for k, v in [("Content-Type", "application/json"), ("Origin", "https://cursor.com"),
             ("Referer", "https://cursor.com/dashboard?tab=usage"),
             ("Cookie", f"WorkosCursorSessionToken={cookie}")]:
    req.add_header(k, v)
try:
    with urllib.request.urlopen(req, timeout=30) as r:
        data = json.loads(r.read())
    total = data.get("totalUsageEventsCount", 0) or 0
    print(f"verify:   OK — {total} usage event(s) in the last 7 days")
    if total == 0:
        print("warning:  zero events; if this account should have usage, check team_id")
except urllib.error.HTTPError as e:
    raise SystemExit(f"verify:   FAILED (HTTP {e.code}) — token expired or wrong account")
PY
}

# ── Output helpers ────────────────────────────────────────────────────────────
write_env() { # $1 = env file. Never echoes the token.
  touch "$1"
  if grep -q '^CURSOR_SESSION_TOKEN=' "$1"; then
    tmp=$(mktemp)
    # grep -v exits 1 when nothing else is in the file; that's still success here
    grep -v '^CURSOR_SESSION_TOKEN=' "$1" >"$tmp" || true
    cat "$tmp" >"$1" && rm -f "$tmp"
  fi
  echo "CURSOR_SESSION_TOKEN=$COOKIE" >>"$1"
  chmod 600 "$1"
  echo "written:  CURSOR_SESSION_TOKEN -> $1"
}

print_yaml() {
  cat <<EOF3

Add to apiary.yaml:

settings:
  cursor_cost:
    enabled: true
    team_id: $TEAM_ID
    user_id: $USER_ID

Then restart the daemon. Token expiry shows up as an auth warning in the
daemon log — re-run this script to refresh.
EOF3
}

# ── Modes ─────────────────────────────────────────────────────────────────────
case "$MODE" in
local)
  extract_token
  build_credentials
  echo "account:  $EMAIL"
  echo "token:    from $SOURCE"
  echo "team_id:  $TEAM_ID"
  echo "user_id:  $USER_ID"
  verify_credentials
  if [ -n "$ENV_FILE" ]; then
    write_env "$ENV_FILE"
  else
    echo "(no .env path given — re-run with the path to your daemon's .env to write it)"
  fi
  print_yaml
  ;;

export)
  extract_token
  build_credentials
  echo "account:  $EMAIL" >&2
  echo "token:    from $SOURCE" >&2
  verify_credentials >&2
  OUT="${EXPORT_FILE:-/dev/stdout}"
  {
    echo "# cursor_cost credentials exported $(date -u +%Y-%m-%dT%H:%M:%SZ) for $EMAIL"
    echo "# import on the daemon machine:"
    echo "#   scripts/cursor-cost-setup.sh --import <this-file> /path/to/daemon/.env"
    echo "CURSOR_SESSION_TOKEN=$COOKIE"
    echo "CURSOR_COST_TEAM_ID=$TEAM_ID"
    echo "CURSOR_COST_USER_ID=$USER_ID"
  } >"$OUT"
  if [ -n "$EXPORT_FILE" ]; then
    chmod 600 "$EXPORT_FILE"
    echo "exported: $EXPORT_FILE (delete it after importing on the daemon machine)" >&2
  fi
  ;;

import)
  if [ "$IMPORT_FILE" = "-" ]; then
    PAYLOAD=$(cat)
  else
    [ -r "$IMPORT_FILE" ] || { echo "error: cannot read $IMPORT_FILE" >&2; exit 1; }
    PAYLOAD=$(cat "$IMPORT_FILE")
  fi
  COOKIE=$(printf '%s\n' "$PAYLOAD" | grep '^CURSOR_SESSION_TOKEN=' | head -1 | cut -d= -f2- || true)
  TEAM_ID=$(printf '%s\n' "$PAYLOAD" | grep '^CURSOR_COST_TEAM_ID=' | head -1 | cut -d= -f2- || true)
  USER_ID=$(printf '%s\n' "$PAYLOAD" | grep '^CURSOR_COST_USER_ID=' | head -1 | cut -d= -f2- || true)
  TEAM_ID="${TEAM_ID:-0}"
  USER_ID="${USER_ID:-0}"
  if [ -z "$COOKIE" ]; then
    echo "error: no CURSOR_SESSION_TOKEN line in the import payload (is this an --export file?)" >&2
    exit 1
  fi
  echo "team_id:  $TEAM_ID"
  echo "user_id:  $USER_ID"
  verify_credentials
  if [ -n "$ENV_FILE" ]; then
    write_env "$ENV_FILE"
  else
    echo "(no .env path given — re-run with the path to your daemon's .env to write it)"
  fi
  print_yaml
  ;;
esac
