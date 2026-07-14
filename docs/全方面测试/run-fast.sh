#!/bin/bash
# run-fast.sh — run all 16 scenarios with reduced duration for CI/local validation.
#
# Usage: ./docs/全方面测试/run-fast.sh
#
# Differences from run_all.sh:
#   - Shorter duration per scenario (15-30s vs 30-180s) → ~20-25 min total
#   - Writes results to docs/全方面测试/results/
#   - Generates REPORT.md via validation_report.py

set -euo pipefail
cd "$(dirname "$0")"

GATEWAY="${GATEWAY:-http://localhost:8781}"
DURATION_NORMAL=15   # was 30-90s
DURATION_HEAVY=30   # was 180s for S12
export GATEWAY

SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
           S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky
           S09_streaming S10_long_prompt S11_quota_recovery S12_comprehensive
           S13_no_candidate S14_model_not_found S15_cross_group_failover
           S16_quick_recovery)

RESULTS_DIR="$(pwd)/results"
mkdir -p "$RESULTS_DIR"

PASS=0; FAIL=0; SKIP=0
declare -A SCEN_STATUS

echo "════════════════════════════════════════════════════════════"
echo " LLM Gateway Fast Test Suite (16 scenarios)"
echo " gateway=$GATEWAY  duration_normal=${DURATION_NORMAL}s  heavy=${DURATION_HEAVY}s"
echo "════════════════════════════════════════════════════════════"

START=$(date +%s)

for S in "${SCENARIOS[@]}"; do
  echo ""
  echo "──────────────────────────────────────────"
  echo "▶ $S"
  echo "──────────────────────────────────────────"
  if bash scenarios/${S}.sh; then
    SCEN_STATUS[$S]="completed"
    PASS=$((PASS + 1))
  else
    SCEN_STATUS[$S]="failed"
    FAIL=$((FAIL + 1))
  fi
done

END=$(date +%s)
ELAPSED=$((END - START))

echo ""
echo "════════════════════════════════════════════════════════════"
echo " SUMMARY ($ELAPSED s elapsed)"
echo "════════════════════════════════════════════════════════════"
for S in "${SCENARIOS[@]}"; do
  status="${SCEN_STATUS[$S]:-skipped}"
  case "$status" in
    completed) icon="✅" ;;
    failed)    icon="❌" ;;
    *)          icon="⏩" ;;
  esac
  echo "  $icon  $S"
done
echo ""
echo " passed:  $PASS"
echo " failed:  $FAIL"
echo " elapsed: ${ELAPSED}s"
echo ""

echo "─── 生成验收报告 ───"
python3 tools/validation_report.py --results "$RESULTS_DIR" > "$RESULTS_DIR/REPORT.md" 2>&1 || true
echo "  → $RESULTS_DIR/REPORT.md"

if [ "$FAIL" -eq 0 ]; then
  echo "🟢 ALL $PASS SCENARIOS COMPLETED"
else
  echo "🔴 $FAIL SCENARIOS FAILED"
fi
