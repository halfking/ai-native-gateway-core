#!/usr/bin/env bash
# 运行已准备好的单个模型测试；不改动 Docker、PostgreSQL、Redis 或 mock 状态。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
RESULTS_DIR="$ROOT/docs/全方面测试/routing-test/results"
MODEL="${1:?usage: run-model.sh <model>}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
API_KEY="${API_KEY:?set API_KEY to an approved local or test key}"
CLIENTS="${CLIENTS:-5}"
ROUNDS="${ROUNDS:-5}"
INTERVAL="${INTERVAL:-10s}"
REQUEST_TIMEOUT="${REQUEST_TIMEOUT:-90s}"
PING_ONLY="${PING_ONLY:-false}"

mkdir -p "$RESULTS_DIR"
timestamp="$(date +%Y%m%d-%H%M%S)"
output="$RESULTS_DIR/${MODEL}-${timestamp}.jsonl"
args=(
    -gateway "$GATEWAY_URL"
    -api-key "$API_KEY"
    -models "$MODEL"
    -clients "$CLIENTS"
    -rounds "$ROUNDS"
    -interval "$INTERVAL"
    -request-timeout "$REQUEST_TIMEOUT"
    -output "$output"
)
if [[ "$PING_ONLY" == "true" ]]; then
    args+=(-ping-only)
fi

cd "$ROOT"
go run ./cmd/routing-test-client "${args[@]}"
printf 'Results: %s\n' "$output"
