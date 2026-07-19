#!/bin/bash
# docs/全方面测试/scenarios/run_all.sh
#
# 一键跑 16 个场景，并生成总报告。
#
# 用法：
#   ./scenarios/run_all.sh --skip-scenarios "S11 S12"  # 跳过特定场景
#   ./scenarios/run_all.sh --fast                      # 全场景时长压缩 50%
#   ./scenarios/run_all.sh --gateway http://localhost:8781
#
# 总运行时间：约 90-120 分钟（默认），fast 模式约 50 分钟。

set -euo pipefail
cd "$(dirname "$0")/.."

# ── 参数 ───────────────────────────────────────────────────────────────
SKIP_SCENARIOS=""
FAST=0
GATEWAY="${GATEWAY:-http://localhost:8781}"
while [ $# -gt 0 ]; do
    case "$1" in
        --skip-scenarios) SKIP_SCENARIOS="$2"; shift 2 ;;
        --fast) FAST=1; shift ;;
        --gateway) GATEWAY="$2"; shift 2 ;;
        *) echo "unknown: $1"; exit 2 ;;
    esac
done

if [ "$FAST" = "1" ]; then
    export DURATION_NORMAL=15
    export DURATION_HEAVY=30
    export DURATION_RECOVERY=15
    export QUOTA_WINDOW_SEC=10
    export QUOTA_WAIT_SEC=12
    echo "fast mode: normal=${DURATION_NORMAL}s heavy=${DURATION_HEAVY}s recovery=${DURATION_RECOVERY}s"
fi

export GATEWAY

echo "===================================================================="
echo " LLM Gateway Full Test Suite — 19 scenarios"
echo " gateway=$GATEWAY"
echo " scenarios to skip: ${SKIP_SCENARIOS:-NONE}"
echo "===================================================================="
echo ""

SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
           S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky
           S09_streaming S10_long_prompt S11_quota_recovery S12_comprehensive
           S13_no_candidate S14_model_not_found S15_cross_group_failover
           S16_quick_recovery S17_stream_continuation S18_null_handling S19_tenant_isolation)

# 先启动 mock_supplier cluster
echo "─── 启动 60 个 mock_supplier ───"
PIDS_DIR="${PIDS_DIR:-/tmp/lab-suppliers}"
export PIDS_DIR
mkdir -p "$PIDS_DIR"
bash tools/start_suppliers.sh
echo ""

# 预备
cd "$(pwd)"
RESULTS_DIR="$(pwd)/results"
mkdir -p "$RESULTS_DIR"

PASS=0
FAIL=0
declare -A SCEN_STATUS

for S in "${SCENARIOS[@]}"; do
    # check skip
    if [ -n "$SKIP_SCENARIOS" ]; then
        if [[ " $SKIP_SCENARIOS " == *" $S "* ]]; then
            echo "⏩  skip: $S"
            continue
        fi
    fi
    echo "──────────────────────────────────────────────────────────────"
    echo "▶  ${S}"
    echo "──────────────────────────────────────────────────────────────"
    if bash scenarios/${S}.sh; then
        echo "✅  $S completed"
        SCEN_STATUS[$S]="completed"
        PASS=$((PASS + 1))
    else
        echo "❌  $S failed (exit=$?)"
        SCEN_STATUS[$S]="failed"
        FAIL=$((FAIL + 1))
    fi
    echo ""
done

echo "===================================================================="
echo " SUMMARY"
echo "===================================================================="
for S in "${SCENARIOS[@]}"; do
    status="${SCEN_STATUS[$S]:-skipped}"
    case "$status" in
        completed) icon="✅" ;;
        failed)    icon="❌" ;;
        *)          icon="⏩" ;;
    esac
    echo "  $icon  ${S}"
done
echo ""
echo " passed:  $PASS"
echo " failed:  $FAIL"
echo ""

# 生成验收报告
echo "─── 生成验收报告 ───"
python3 tools/validation_report.py --results "$RESULTS_DIR" > "$RESULTS_DIR/REPORT.md" 2>&1 || true
echo "  → $RESULTS_DIR/REPORT.md"
echo ""

# 最后 cleanup
echo "─── 清理 ───"
export PIDS_DIR="${PIDS_DIR:-/tmp/lab-suppliers}"
bash tools/start_suppliers.sh stop
echo ""

if [ "$FAIL" -eq 0 ]; then
    echo "🟢 ALL $PASS SCENARIOS COMPLETED"
    exit 0
else
    echo "🔴 $FAIL SCENARIOS FAILED"
    exit 1
fi
