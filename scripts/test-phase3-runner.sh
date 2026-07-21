#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/scripts/multimodal-e2e/run_phase3.sh"
TMP="$(mktemp -d -t phase3-runner-test.XXXXXX)"
PORT_FILE="$TMP/port"
trap '[[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT

cat > "$TMP/server.py" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

mode = sys.argv[1]

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        model = ""
        try:
            model = json.loads(body).get("model", "")
        except Exception:
            pass
        if mode == "no_candidate":
            status = 503
            payload = {"error": {"code": "no_candidate", "message": "No available provider"}}
        elif mode == "generic_503":
            status = 503
            payload = {"error": {"code": "upstream_unavailable", "message": "temporarily unavailable"}}
        elif mode == "gpt35_reject":
            if model == "gpt-3.5-turbo":
                status = 503
                payload = {"error": {"code": "no_candidate", "message": "No available provider for model 'gpt-3.5-turbo'"}}
            else:
                status = 200
                payload = {"choices": [{"message": {"content": model or "ok"}}]}
        else:
            status = 200
            payload = {"choices": [{"message": {"content": model or "ok"}}]}
        encoded = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, *_):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
PY

start_server() {
  local mode="$1"
  : > "$PORT_FILE"
  python3 "$TMP/server.py" "$mode" > "$PORT_FILE" &
  SERVER_PID=$!
  for _ in $(seq 1 50); do
    [[ -s "$PORT_FILE" ]] && break
    sleep 0.05
  done
  GATEWAY_URL="http://127.0.0.1:$(cat "$PORT_FILE")"
}

stop_server() {
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  unset SERVER_PID
}

run_runner() {
  env LLM_GATEWAY_API_KEY=test-key GATEWAY_URL="$GATEWAY_URL" LOG_FILE="$TMP/runner.log" bash "$RUNNER" "$@"
}

run_runner_env() {
  env LLM_GATEWAY_API_KEY=test-key GATEWAY_URL="$GATEWAY_URL" LOG_FILE="$TMP/runner.log" "$@" bash "$RUNNER"
}

start_server success
output=$(run_runner_env ONLY_IDS=T-02 MODEL_OVERRIDE_T_02=override-vision 2>&1)
stop_server
grep -q 'T-02.*PASS (override-vision' <<<"$output"
grep -q 'skip: 4' <<<"$output"

start_server no_candidate
output=$(run_runner_env ONLY_IDS=T-15 2>&1)
stop_server
grep -q 'T-15.*PASS (expected reject, http=503, code=no_candidate)' <<<"$output"

start_server generic_503
set +e
output=$(run_runner_env ONLY_IDS=T-15 2>&1)
rc=$?
set -e
stop_server
[[ $rc -ne 0 ]]
grep -q 'T-15.*FAIL (expected reject, got http=503)' <<<"$output"

printf '%s' '{"error":{"code":"no_candidate","message":"No available provider stale"}}' > /tmp/case.out
output=$(run_runner_env ONLY_IDS=T-02 2>&1 || true)
grep -q 'T-02.*FAIL.*http=000' <<<"$output"
if grep -q 'No available provider' <<<"$output"; then
  echo "stale response body leaked into curl failure output" >&2
  exit 1
fi

output=$(run_runner_env SKIP_IDS=T-05 ONLY_IDS=T-05 2>&1)
grep -q 'T-05.*SKIP' <<<"$output"

start_server success
output=$(run_runner --id T-02 2>&1)
stop_server
grep -q 'T-02.*PASS' <<<"$output"
grep -q 'skip: 4' <<<"$output"

echo "T-02 T-15" > "$TMP/ids.txt"
start_server gpt35_reject
output=$(run_runner --id-file "$TMP/ids.txt" 2>&1)
stop_server
grep -q 'T-02.*PASS' <<<"$output"
grep -q 'T-15.*PASS (expected reject, http=503, code=no_candidate)' <<<"$output"
if grep -q 'T-03.*PASS\|T-04.*PASS' <<<"$output"; then
  echo "--id-file did not restrict to listed ids" >&2
  exit 1
fi

echo "phase3 runner tests: PASS"
