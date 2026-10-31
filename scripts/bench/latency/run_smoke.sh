#!/usr/bin/env bash
# Local AL smoke: mock_pair + measure_added_latency. No production hosts.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$DIR/lib.sh"

# Negative control: blocked host must refuse
if GW_BASE="http://120.78.136.252:8080" latency_bench_assert_safe_env 2>/dev/null; then
  echo "[smoke] FAIL: blocked host was accepted" >&2
  exit 1
else
  echo "[smoke] OK: production host blacklist refuses 252"
fi

READY="$(mktemp -t latency-bench-ready.XXXXXX)"
OUT="$(mktemp -t latency-bench-out.XXXXXX)"
UP_PORT=19091
GW_PORT=18091
OVERHEAD=15

cleanup() {
  if [[ -n "${MOCK_PID:-}" ]] && kill -0 "$MOCK_PID" 2>/dev/null; then
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi
  rm -f "$READY" "$OUT"
}
trap cleanup EXIT

python3 "$DIR/mock_pair.py" \
  --upstream-port "$UP_PORT" \
  --gateway-port "$GW_PORT" \
  --upstream-ms 40 \
  --overhead-ms "$OVERHEAD" \
  --ready-file "$READY" &
MOCK_PID=$!

for _ in $(seq 1 50); do
  [[ -s "$READY" ]] && break
  sleep 0.1
done
[[ -s "$READY" ]] || { echo "[smoke] FAIL: mock not ready"; exit 1; }

export GW_BASE="http://127.0.0.1:${GW_PORT}"
export UPSTREAM_BASE="http://127.0.0.1:${UP_PORT}"
latency_bench_assert_safe_env

python3 "$DIR/measure_added_latency.py" \
  --gateway-base "$GW_BASE" \
  --upstream-base "$UPSTREAM_BASE" \
  --requests 30 \
  --concurrency 6 \
  --warmup 3 \
  --output "$OUT" \
  --expect-added-p95-min-ms 5 \
  --expect-added-p95-max-ms 80

echo "[smoke] report:"
cat "$OUT"
echo "[smoke] PASS"
