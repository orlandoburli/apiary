#!/usr/bin/env bash
# cursor-cost-setup.sh — wire up settings.cursor_cost on the machine where the
# Cursor CLI is logged in.
#
# Extracts the Cursor session token (macOS keychain, or the IDE's state.vscdb
# as a fallback), discovers team_id/user_id from ~/.cursor/cli-config.json,
# verifies the credentials against the dashboard API, and writes
# CURSOR_SESSION_TOKEN into a .env file.
#
# Usage:
#   scripts/cursor-cost-setup.sh [path/to/.env]
#
# With no argument the .env line and the apiary.yaml block are printed only.
# Requires: python3; macOS `security` for the keychain path.
set -euo pipefail

ENV_FILE="${1:-}"
CLI_CONFIG="$HOME/.cursor/cli-config.json"

# ── 1. Token: prefer the CLI's keychain entry (the account that actually runs
#       cursor-agent), fall back to the IDE's local state DB.
TOKEN=""
SOURCE=""
if command -v security >/dev/null 2>&1; then
  TOKEN=$(security find-generic-password -s cursor-access-token -w 2>/dev/null || true)
  [ -n "$TOKEN" ] && SOURCE="macOS keychain (cursor-access-token — the CLI's login)"
fi
if [ -z "$TOKEN" ]; then
  for DB in "$HOME/Library/Application Support/Cursor/User/globalStorage/state.vscdb" \
            "$HOME/.config/Cursor/User/globalStorage/state.vscdb"; do
    if [ -f "$DB" ]; then
      TOKEN=$(sqlite3 "$DB" "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'" 2>/dev/null | tr -d '"' || true)
      [ -n "$TOKEN" ] && SOURCE="Cursor IDE state.vscdb — NOTE: the IDE login, which may differ from the CLI's account" && break
    fi
  done
fi
if [ -z "$TOKEN" ]; then
  echo "error: no Cursor session token found (is the Cursor CLI or IDE logged in on this machine?)" >&2
  exit 1
fi

# ── 2. Account ids + cookie. The cookie is "<user-part-of-authId>%3A%3A<jwt>";
#       authId/teamId/userId come from the CLI config, with the JWT's sub as a
#       fallback for the user part.
read -r COOKIE TEAM_ID USER_ID EMAIL <<EOF2
$(TOKEN="$TOKEN" CLI_CONFIG="$CLI_CONFIG" python3 <<'PY'
import base64, json, os

tok = os.environ["TOKEN"].strip()
auth = {}
try:
    auth = json.load(open(os.environ["CLI_CONFIG"]))["authInfo"]
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

echo "account:  $EMAIL"
echo "token:    from $SOURCE"
echo "team_id:  $TEAM_ID"
echo "user_id:  $USER_ID"

# ── 3. Verify against the dashboard API before writing anything.
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

# ── 4. Write or print the result. Never echo the token to the terminal.
if [ -n "$ENV_FILE" ]; then
  touch "$ENV_FILE"
  if grep -q '^CURSOR_SESSION_TOKEN=' "$ENV_FILE"; then
    tmp=$(mktemp)
    # grep -v exits 1 when nothing else is in the file; that's still success here
    grep -v '^CURSOR_SESSION_TOKEN=' "$ENV_FILE" >"$tmp" || true
    cat "$tmp" >"$ENV_FILE" && rm -f "$tmp"
  fi
  echo "CURSOR_SESSION_TOKEN=$COOKIE" >>"$ENV_FILE"
  chmod 600 "$ENV_FILE"
  echo "written:  CURSOR_SESSION_TOKEN -> $ENV_FILE"
else
  echo "(no .env path given — re-run with the path to your daemon's .env to write it)"
fi

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
