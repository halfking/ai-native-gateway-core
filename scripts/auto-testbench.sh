#!/usr/bin/env bash
# auto-testbench.sh — AUTO 路由专项测试统一入口（v2 规划 P0①，2026-09-24）。
#
# 编排两层评测（docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §4.3）：
#   1. 离线回归（默认，无网络/无 DB）：cmd/auto-testbench 对
#      autoroute/testdata 三份套件跑启发式分类回归，产出
#      reports/auto-testbench.{json,md,cases.jsonl}，并对照随仓基线
#      cmd/auto-testbench/testdata/baseline.json 做回归门禁（低于阈值 exit 1）。
#   2. E2E 审计（可选，AUTO_E2E=1）：复用 cmd/autoroute-e2e-audit 打真实
#      网关（-suite 指定同一套件），结果 JSONL 交 testbench 合并进报告。
#
# 用法：
#   scripts/auto-testbench.sh                  # 离线回归 + 门禁
#   scripts/auto-testbench.sh --no-gate        # 只跑回归不卡门禁
#   scripts/auto-testbench.sh --refresh-baseline   # 重写随仓基线（调参/档案变更后；跳过门禁）
#   AUTO_E2E=1 AUTO_AUDIT_API_KEY=... scripts/auto-testbench.sh   # 离线+E2E
#
# E2E 环境变量：
#   AUTO_E2E=1                 启用 E2E 层
#   AUTO_AUDIT_API_KEY=<key>   网关 API key（必填，同 autoroute-e2e-audit）
#   E2E_GATEWAY=<url>          网关地址（默认 http://127.0.0.1:8782）
#
# 套件候选生成（"修正即测试"，独立于回归，需只读 DSN）：
#   go run ./cmd/auto-testbench -mode generate -source corrections \
#     -dsn "$DATABASE_URL" -out /tmp/candidates.jsonl
set -euo pipefail
cd "$(dirname "$0")/.."

GATE=1
REFRESH_BASELINE=0
for arg in "$@"; do
  case "$arg" in
    --no-gate) GATE=0 ;;
    --refresh-baseline) REFRESH_BASELINE=1 ;;
    *) echo "unknown flag: $arg (supported: --no-gate, --refresh-baseline)" >&2; exit 2 ;;
  esac
done

mkdir -p reports

# R64（P2）修复：refresh-baseline 模式必须跳过门禁。基线是"调参前指标"的
# 快照，调参后旧基线的阈值必然不再匹配新指标——若照旧先跑 -gate-default，
# 门禁 FAIL（exit 1）会在 set -e 下直接中止脚本，--refresh-baseline 永远
# 不可达。因此 refresh 模式下本次运行不挂门禁（回归本身仍全量执行），并把
# -write-baseline 合并进同一次运行，保证写盘基线与刚产出的报告是同一份指
# 标。非 refresh 模式维持原语义：回归 → 门禁 → FAIL exit 1。
GATE_FLAGS=""
WRITE_BASELINE_FLAGS=""
if [ "$GATE" = "1" ] && [ "$REFRESH_BASELINE" = "0" ]; then
  GATE_FLAGS="-gate-default"
fi
if [ "$REFRESH_BASELINE" = "1" ]; then
  WRITE_BASELINE_FLAGS="-write-baseline cmd/auto-testbench/testdata/baseline.json"
fi

E2E_MERGE_FLAG=""
if [ "${AUTO_E2E:-0}" = "1" ]; then
  if [ -z "${AUTO_AUDIT_API_KEY:-}" ]; then
    echo "AUTO_E2E=1 requires AUTO_AUDIT_API_KEY" >&2
    exit 2
  fi
  E2E_GATEWAY_URL="${E2E_GATEWAY:-http://127.0.0.1:8782}"
  echo "== E2E audit against ${E2E_GATEWAY_URL} (suite v1+v2+v3) =="
  go run ./cmd/autoroute-e2e-audit \
    -gateway "${E2E_GATEWAY_URL}" \
    -suite autoroute/testdata/auto_matching_suite.jsonl \
    -out reports/auto-testbench-e2e.jsonl
  # v2/v3 追加跑（e2e-audit 单文件参数），输出按行拼接。
  go run ./cmd/autoroute-e2e-audit \
    -gateway "${E2E_GATEWAY_URL}" \
    -suite autoroute/testdata/auto_matching_suite_v2.jsonl \
    -out reports/auto-testbench-e2e-v2.jsonl
  go run ./cmd/autoroute-e2e-audit \
    -gateway "${E2E_GATEWAY_URL}" \
    -suite autoroute/testdata/auto_matching_suite_v3.jsonl \
    -out reports/auto-testbench-e2e-v3.jsonl
  cat reports/auto-testbench-e2e-v2.jsonl reports/auto-testbench-e2e-v3.jsonl >> reports/auto-testbench-e2e.jsonl
  E2E_MERGE_FLAG="-e2e-report reports/auto-testbench-e2e.jsonl"
fi

echo "== offline regression (240 cases, heuristic:default) =="
go run ./cmd/auto-testbench \
  -mode regression \
  -report reports/auto-testbench \
  ${GATE_FLAGS} ${E2E_MERGE_FLAG} ${WRITE_BASELINE_FLAGS}
if [ "$REFRESH_BASELINE" = "1" ]; then
  echo "baseline refreshed: cmd/auto-testbench/testdata/baseline.json"
fi

echo "reports: reports/auto-testbench.md (+ .json / .cases.jsonl)"
