#!/usr/bin/env bash
# =====================================================================
# tests/deploy_credential_decrypt_verify_test.sh — offline behavioral
# coverage of deploy_verify_credential_decrypt (post-deploy-verify.sh).
#
# 2026-09-04 245 incident gate: a blue-green candidate can pass
# healthz / readyz / background-tasks while EVERY credential fails to
# decrypt (stale binary missing the DecryptAny fix). The verify
# function must catch that via the admin API and return 1 so
# deploy-seamless.sh rolls the candidate back.
#
# Strategy: stand up a local python3 mock of the admin API
# (/api/auth/token, /api/providers, /api/providers/<id>/credentials)
# driven by a per-case fixture file, and a fake ssh_cmd that executes
# the "remote" command locally — the function's remote python then
# talks to 127.0.0.1:<mock_port> directly. No network, no real hosts.
#
# Cases:
#   AC-CD-1  all credentials decrypt          → rc 0 (OK)
#   AC-CD-2  every credential decrypt_failed  → rc 1 (FAIL, rollback)
#   AC-CD-3  partial failures                 → rc 0 (WARN, non-blocking)
#   AC-CD-4  providers exist, no credentials  → rc 0 (WARN skip)
#   AC-CD-5  env file missing                 → rc 1 (FAIL, fail closed)
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../scripts/deploy-lib/post-deploy-verify.sh
source "$REPO_ROOT/scripts/deploy-lib/post-deploy-verify.sh"

MOCK_PORT=18781
MOCK_FIXTURE="$(mktemp -t cred_decrypt_fixture.XXXXXX).json"
MOCK_ENV="$(mktemp -t cred_decrypt_env.XXXXXX)"
MOCK_LOG="$(mktemp -t cred_decrypt_mock.XXXXXX.log)"
MOCK_PID=""

cleanup() {
  [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null
  rm -f "$MOCK_FIXTURE" "$MOCK_ENV" "$MOCK_LOG"
}
trap cleanup EXIT

printf 'LLM_GATEWAY_ADMIN_USER=admin\nLLM_GATEWAY_ADMIN_PASSWORD=mock-pass\n' >"$MOCK_ENV"

# ---- mock admin API --------------------------------------------------
python3 - "$MOCK_PORT" "$MOCK_FIXTURE" >"$MOCK_LOG" 2>&1 <<'MOCK' &
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
FIXTURE = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _reply(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path == '/api/auth/token':
            self._reply({'access_token': 'mock-token'})
        else:
            self._reply({'error': 'not found'}, 404)

    def do_GET(self):
        with open(FIXTURE, encoding='utf-8') as handle:
            fixture = json.load(handle)
        if self.path == '/api/providers':
            self._reply(fixture['providers'])
            return
        prefix = '/api/providers/'
        suffix = '/credentials'
        if self.path.startswith(prefix) and self.path.endswith(suffix):
            pid = self.path[len(prefix):-len(suffix)]
            creds = fixture['credentials'].get(pid)
            if creds is None:
                self._reply({'error': 'provider not found'}, 404)
            else:
                self._reply(creds)
            return
        self._reply({'error': 'not found'}, 404)


HTTPServer(('127.0.0.1', PORT), Handler).serve_forever()
MOCK
MOCK_PID=$!

# Wait for the mock to accept connections (max ~5s).
for _ in $(seq 1 50); do
  if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:${MOCK_PORT}/api/providers"; then
    break
  fi
  sleep 0.1
done

# The verify function builds SSH commands as: $ssh_cmd "<remote-cmd>".
# Execute them locally so the embedded python hits the local mock.
ssh_cmd() {
  bash -c "$1"
}

write_fixture() {
  cp "$1" "$MOCK_FIXTURE"
}

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

run_case() {
  local name=$1 fixture=$2 want_rc=$3 want_log=$4
  echo "── ${name} ──"
  write_fixture "$fixture"
  local out rc=0
  out=$(DEPLOY_VERIFY_GREEN='' DEPLOY_VERIFY_RED='' DEPLOY_VERIFY_YELLOW='' DEPLOY_VERIFY_NC='' \
    deploy_verify_credential_decrypt ssh_cmd "$MOCK_PORT" "$MOCK_ENV" 2>&1) || rc=$?
  if [[ "$rc" == "$want_rc" && "$out" == *"$want_log"* ]]; then
    echo "  pass (rc=$rc)"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    echo "  FAIL: want rc=$want_rc log~'$want_log', got rc=$rc"
    echo "$out" | sed 's/^/    /'
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILED_NAMES+=("$name")
  fi
}

# ---- AC-CD-1: all decrypt → OK --------------------------------------
F1=$(mktemp -t cred_fix1.XXXXXX.json)
cat >"$F1" <<'JSON'
{
  "providers": [
    {"id": 18, "active_credential_count": 2},
    {"id": 1,  "active_credential_count": 1},
    {"id": 7,  "active_credential_count": 0}
  ],
  "credentials": {
    "18": [
      {"id": 101, "key_masked": "sk-a...1111", "key_mask_error": null},
      {"id": 102, "key_masked": "sk-b...2222", "key_mask_error": null}
    ],
    "1": [
      {"id": 103, "key_masked": "sk-c...3333", "key_mask_error": null}
    ]
  }
}
JSON
run_case "AC-CD-1 all-decrypt → OK" "$F1" 0 "creds=3 failed=0"

# ---- AC-CD-2: every credential decrypt_failed → FAIL ----------------
F2=$(mktemp -t cred_fix2.XXXXXX.json)
cat >"$F2" <<'JSON'
{
  "providers": [
    {"id": 587, "active_credential_count": 2}
  ],
  "credentials": {
    "587": [
      {"id": 201, "key_masked": null, "key_mask_error": "decrypt_failed"},
      {"id": 202, "key_masked": null, "key_mask_error": "decrypt_failed"}
    ]
  }
}
JSON
run_case "AC-CD-2 all-failed → FAIL (rollback)" "$F2" 1 "all_credentials_undecryptable"

# ---- AC-CD-3: partial failures → WARN, rc 0 ---------------------------
F3=$(mktemp -t cred_fix3.XXXXXX.json)
cat >"$F3" <<'JSON'
{
  "providers": [
    {"id": 18, "active_credential_count": 2}
  ],
  "credentials": {
    "18": [
      {"id": 301, "key_masked": "sk-d...4444", "key_mask_error": null},
      {"id": 302, "key_masked": null, "key_mask_error": "decrypt_failed"}
    ]
  }
}
JSON
run_case "AC-CD-3 partial → WARN non-blocking" "$F3" 0 "partial_failures_likely_legacy_rows"

# ---- AC-CD-4: providers but zero credentials → WARN skip -------------
F4=$(mktemp -t cred_fix4.XXXXXX.json)
cat >"$F4" <<'JSON'
{
  "providers": [
    {"id": 9, "active_credential_count": 0}
  ],
  "credentials": {
    "9": []
  }
}
JSON
run_case "AC-CD-4 no creds → WARN skip" "$F4" 0 "no_credentials_to_scan"

# ---- AC-CD-5: env file unreadable → FAIL, fail closed -----------------
echo "── AC-CD-5 env-file-missing → FAIL ──"
RC=0
OUT=$(DEPLOY_VERIFY_GREEN='' DEPLOY_VERIFY_RED='' DEPLOY_VERIFY_YELLOW='' DEPLOY_VERIFY_NC='' \
  deploy_verify_credential_decrypt ssh_cmd "$MOCK_PORT" "/nonexistent/env" 2>&1) || RC=$?
if [[ "$RC" == 1 && "$OUT" == *"env_file_unreadable"* ]]; then
  echo "  pass (rc=$RC)"
  TESTS_PASSED=$((TESTS_PASSED + 1))
else
  echo "  FAIL: want rc=1 log~'env_file_unreadable', got rc=$RC"
  echo "$OUT" | sed 's/^/    /'
  TESTS_FAILED=$((TESTS_FAILED + 1))
  FAILED_NAMES+=("AC-CD-5")
fi

rm -f "$F1" "$F2" "$F3" "$F4"

echo
echo "credential-decrypt verify: passed=$TESTS_PASSED failed=$TESTS_FAILED"
if (( TESTS_FAILED > 0 )); then
  printf 'failed: %s\n' "${FAILED_NAMES[*]}"
  exit 1
fi
exit 0
