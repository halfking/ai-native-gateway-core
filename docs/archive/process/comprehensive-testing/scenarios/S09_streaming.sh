#!/bin/bash
# S09: 流式 SSE — broken_stream 故障自动重试
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S09] streaming: 50% stream ratio, broken_stream on G"
set_group G broken_stream
run_loadtest S09_streaming \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tok3 --prompt short \
    --stream-ratio 0.5
print_summary S09_streaming
reset_all_suppliers
echo "  期望：流完成率 >95%, broken_stream 自动重试"
