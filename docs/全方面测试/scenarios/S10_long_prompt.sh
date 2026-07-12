#!/bin/bash
# S10: 长 Prompt — 大上下文，E 组 context_length 触发
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S10] long prompt: 40 clients × long prompt + E=context_too_long"
set_group E context_too_long
run_loadtest S10_long \
    --n-clients 40 --rps-per-client 5 --duration 60 \
    --models tok3 --prompt long
print_summary S10_long
reset_all_suppliers
echo "  期望：>95% 成功（context_too_long 应该 failover）"
