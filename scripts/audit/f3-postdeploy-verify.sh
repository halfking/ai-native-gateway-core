#!/bin/bash
# F3 残留地平线 —— **部署后验收**（运行时层）
#
# ★ 为什么要有这个脚本（2026-11-17 第三轮）：
#   迁移 972 的**产物探针**（scripts/apply-db-revision-sequence.sh 里的
#   ledger_artifact_probe "972_..."）原本还断言「库内没有残留行」——
#   那是**运行时状态**，不是迁移产物 ⇒ 它在 211 上把部署打死了（FATAL，
#   2026-11-17 17:0x）⇒ 已从产物层摘除，改由本脚本在**运行时层**判。
#
#   分层：
#     · 产物层（探针）      —— 只判「迁移的产物在不在场」
#     · 写入侧层（AST 门）  —— 判「没有写路径能制造新的矛盾行」
#     · 运行时层（本脚本）  —— 判「残留归零了」且「不再反弹」
#
# 用法：
#   bash scripts/audit/f3-postdeploy-verify.sh [--wait-minutes N] [--settle-minutes M] [--interval-seconds S]
#
#   --wait-minutes N      等残留归零的最长分钟数（默认 30）
#   --settle-minutes M    归零后继续盯多少分钟确认不再反弹（默认 10）
#   --interval-seconds S  轮询间隔秒数（默认 60）
#
# 退出码：
#   0 = 在等待窗口内归零，且在观察窗口内保持 0（不再反弹）
#   1 = 超时仍未归零，或归零后又反弹
#   2 = 取数失败（无法判定）
#
# ★ 口径与 scripts/audit/f3-auto-check.sh 完全一致（同一个谓词）：
#   unavailable_recover_at IS NOT NULL AND unavailable_at IS NULL AND available = TRUE
#   ⇒ 统计的是「**已恢复、但恢复标记没被清掉**的残留行」，不是「不可用节点数」。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$REPO_ROOT/logs/f3-checks"
mkdir -p "$LOG_DIR"

WAIT_MINUTES=30
SETTLE_MINUTES=10
INTERVAL_SECONDS=60

while [[ $# -gt 0 ]]; do
  case "$1" in
    --wait-minutes)    WAIT_MINUTES="$2"; shift 2 ;;
    --settle-minutes)  SETTLE_MINUTES="$2"; shift 2 ;;
    --interval-seconds) INTERVAL_SECONDS="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

TIMESTAMP=$(date '+%Y%m%d-%H%M%S')
LOG_FILE="$LOG_DIR/f3-postdeploy-${TIMESTAMP}.log"
exec > >(tee -a "$LOG_FILE") 2>&1

echo "=========================================="
echo "F3 残留地平线 —— 部署后验收（运行时层）"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S %Z')"
echo "参数: 等待归零 ≤${WAIT_MINUTES}min / 归零后观察 ${SETTLE_MINUTES}min / 轮询 ${INTERVAL_SECONDS}s"
echo "=========================================="
echo ""

# 取当前残留行数（与 f3-auto-check.sh 同谓词、同取数通道）
residual_count() {
  bash "$REPO_ROOT/scripts/audit/q-211-readonly-sql.sh" "
SELECT COUNT(*) FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL
  AND unavailable_at IS NULL
  AND available = TRUE;
" 2>&1 | grep -E '^[0-9]+$' | tail -1
}

sample() {
  local n
  n="$(residual_count)"
  if [[ -z "$n" ]]; then
    echo "❌ 取数失败：无法判定残留数（查询通道异常）"
    exit 2
  fi
  printf '%s' "$n"
}

START_EPOCH=$(date +%s)
WAIT_DEADLINE=$((START_EPOCH + WAIT_MINUTES * 60))

echo "## 阶段一：等待残留归零"
echo ""

ZERO_AT=""
while :; do
  NOW_EPOCH=$(date +%s)
  N="$(sample)"
  ELAPSED=$((NOW_EPOCH - START_EPOCH))
  printf '[%s] 残留=%s 行（已过 %ss / 等待窗口 %ss）\n' \
    "$(date '+%H:%M:%S')" "$N" "$ELAPSED" "$((WAIT_MINUTES * 60))"
  if [[ "$N" == "0" ]]; then
    ZERO_AT="$(date +%s)"
    echo "✅ 已归零（用时 ${ELAPSED}s）"
    break
  fi
  if [[ "$NOW_EPOCH" -ge "$WAIT_DEADLINE" ]]; then
    echo "❌ 等待窗口内未归零：仍有 ${N} 行（用时 ${ELAPSED}s）"
    echo "   ⇒ 写入侧还有能制造矛盾行的路径，或新二进制尚未生效"
    exit 1
  fi
  sleep "$INTERVAL_SECONDS"
done

echo ""
echo "## 阶段二：观察 ${SETTLE_MINUTES} 分钟，确认不再反弹"
echo ""

SETTLE_DEADLINE=$((ZERO_AT + SETTLE_MINUTES * 60))
while :; do
  NOW_EPOCH=$(date +%s)
  N="$(sample)"
  ELAPSED=$((NOW_EPOCH - ZERO_AT))
  printf '[%s] 残留=%s 行（归零后 %ss / 观察窗口 %ss）\n' \
    "$(date '+%H:%M:%S')" "$N" "$ELAPSED" "$((SETTLE_MINUTES * 60))"
  if [[ "$N" != "0" ]]; then
    echo "❌ 归零后又反弹：${N} 行（归零后 ${ELAPSED}s）"
    exit 1
  fi
  if [[ "$NOW_EPOCH" -ge "$SETTLE_DEADLINE" ]]; then
    echo "✅ 观察窗口内保持 0，未反弹"
    break
  fi
  sleep "$INTERVAL_SECONDS"
done

echo ""
echo "=========================================="
echo "部署后验收通过：残留归零且在 ${SETTLE_MINUTES} 分钟观察窗内未反弹"
echo "日志: $LOG_FILE"
echo "=========================================="
