#!/bin/bash
# S14: 模型不存在 — 请求不存在的模型，应 404 model_not_found 且响应快
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S14] nonexistent model: loadtest-nonexistent-xyz (40 client × 5 rounds)"
# Single direct curl test (期望 <50ms 响应)
echo "  direct curl:"
time curl -sS -m 5 -o /tmp/s14.json -w "    http=%{http_code} time=%{time_total}s\n" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-loadtest-01-hash-0000000000000000000000000000000000000001" \
  -d '{"model":"loadtest-nonexistent-xyz","messages":[{"role":"user","content":"x"}],"max_tokens":10}' \
  "$GATEWAY/v1/chat/completions"
cat /tmp/s14.json; echo
echo ""
# 批量压测
run_loadtest S14_model_not_found \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models loadtest-nonexistent-xyz --prompt short
print_summary S14_model_not_found
echo "  期望：100% 错误, P95 < 50ms, 无上游调用"
