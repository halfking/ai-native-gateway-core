#!/bin/bash
# S13: 无可用节点 — 所有供应商故障 → no_candidate
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S13] no candidate: 全部 12 组 server_error"
for g in A B C D E F G H I J K L; do
    set_group "$g" server_error
done

# 2026-08-05 修复：mock_orchestrator.set-group 是异步生效的。
# loadtest 启动瞬间（0~2s）残留 healthy supplier 会承接头几批请求，
# 导致实测有 ~3.5% 泄漏。等 mock_orchestrator 触达所有 60 个 supplier
# 实例的 in-memory state 后再启动 loadtest。
echo "[S13] waiting 3s for mock_orchestrator state to settle across 60 supplier instances..."
sleep 3

# 验证 health-matrix 反映 12 组全是 server_error
echo "[S13] health-matrix after settle:"
python3 "$TOOLS_DIR/mock_orchestrator.py" health-matrix 2>&1 | head -20

run_loadtest S13_no_candidate \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tok3 --prompt short
print_summary S13_no_candidate

reset_all_suppliers
echo "  期望：100% 失败 (503)，网关不崩溃"
