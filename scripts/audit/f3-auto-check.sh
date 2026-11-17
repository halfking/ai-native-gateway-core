#!/bin/bash
# F3 残留地平线自动检查脚本
# 用途：定期检查 211 生产环境的节点状态残留地平线数量

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$REPO_ROOT/logs/f3-checks"
mkdir -p "$LOG_DIR"

TIMESTAMP=$(date '+%Y%m%d-%H%M%S')
LOG_FILE="$LOG_DIR/f3-check-${TIMESTAMP}.log"

# 输出到终端和日志文件
exec > >(tee -a "$LOG_FILE") 2>&1

echo "=========================================="
echo "F3 残留地平线自动检查"
echo "时间: $(date '+%Y-%m-%d %H:%M:%S %Z')"
echo "=========================================="
echo ""

# 检查当前残留数量
echo "## 查询残留地平线数量..."
CURRENT=$(bash "$REPO_ROOT/scripts/audit/q-211-readonly-sql.sh" "
SELECT COUNT(*) FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL 
  AND unavailable_at IS NULL 
  AND available = TRUE;
" 2>&1 | grep -E "^[0-9]+$" | tail -1)

if [[ -z "$CURRENT" ]]; then
  echo "❌ 查询失败，无法获取残留数量"
  exit 1
fi

echo "当前残留数量: ${CURRENT} 行"
echo ""

# 读取历史基线（如果存在）
BASELINE_FILE="$LOG_DIR/baseline.txt"
if [[ -f "$BASELINE_FILE" ]]; then
  BASELINE=$(cat "$BASELINE_FILE")
  echo "历史基线: ${BASELINE} 行"
  
  if [[ "$CURRENT" -lt "$BASELINE" ]]; then
    CLEARED=$((BASELINE - CURRENT))
    RATE=$(awk "BEGIN {printf \"%.1f\", ($CLEARED/$BASELINE)*100}")
    echo "✅ 下降了 ${CLEARED} 行（${RATE}%）"
  elif [[ "$CURRENT" -eq "$BASELINE" ]]; then
    echo "⏸️  数量保持不变"
  else
    INCREASED=$((CURRENT - BASELINE))
    echo "⚠️  增加了 ${INCREASED} 行"
  fi
else
  echo "ℹ️  首次检查，建立基线"
fi

# 更新基线
echo "$CURRENT" > "$BASELINE_FILE"
echo ""

# 按凭据分布
echo "## 按凭据分布（Top 5）"
bash "$REPO_ROOT/scripts/audit/q-211-readonly-sql.sh" "
SELECT 
  credential_id,
  COUNT(*) as stale_count
FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL 
  AND unavailable_at IS NULL 
  AND available = TRUE
GROUP BY credential_id
ORDER BY stale_count DESC
LIMIT 5;
" 2>&1 | tail -8
echo ""

# 评估与建议
echo "## 评估"
# ★ 部署基线 = **F3 根修上线时刻**（2026-11-17 17:44，211 槽 a 接管，
#   版本 2.6.0-f6f99545-20261117-211 / build_seq 3092）。
#   ★ 为什么基线要跟着根修走：告警条件是「残留 > 100 **且** 部署后 > 6h」——
#     基线若停在根修之前的 02:45，则「部署后 > 6h」在 08:45 之后**恒真**
#     ⇒ 这个条件退化成「残留 > 100」⇒ 在「根修后仍反弹」时**没有时间缓冲**。
#   ★ 两次基线都留档：
#     · 02:45 ＝ F3 监控上线（原始部署；根修前的 131→231 反弹窗口属正常）
#     · 17:44 ＝ F3 根修上线（此后残留应恒为 0；> 100 ⇒ 根修未生效或复发）
DEPLOY_TIME="2026-11-17 17:44"
HOURS_SINCE_DEPLOY=$(( ($(date +%s) - $(date -j -f "%Y-%m-%d %H:%M" "$DEPLOY_TIME" +%s 2>/dev/null || echo 0)) / 3600 ))

if [[ "$CURRENT" -le 10 ]]; then
  echo "✅ 残留数量已降至低水位 (${CURRENT} ≤ 10)"
  echo "   F3 修复效果良好，继续保持监控"
elif [[ "$CURRENT" -le 50 ]]; then
  echo "✓ 残留数量正常 (${CURRENT} ≤ 50)"
  if [[ "$HOURS_SINCE_DEPLOY" -lt 6 ]]; then
    echo "   部署后 ${HOURS_SINCE_DEPLOY} 小时，仍在下降期，继续观察"
  else
    echo "   部署后 ${HOURS_SINCE_DEPLOY} 小时，建议继续观察是否继续下降"
  fi
elif [[ "$CURRENT" -le 100 ]]; then
  echo "⚠️  残留数量偏高 (${CURRENT} > 50)"
  if [[ "$HOURS_SINCE_DEPLOY" -lt 6 ]]; then
    echo "   部署后 ${HOURS_SINCE_DEPLOY} 小时，继续观察"
  else
    echo "   建议检查 credentialhealth 恢复任务是否正常运行"
  fi
else
  echo "⚠️  残留数量较高 (${CURRENT} > 100)"
  echo "   建议："
  echo "   1. 检查 credentialhealth 恢复任务日志"
  echo "   2. 检查主要凭据的状态（availability_state）"
  echo "   3. 必要时人工介入"
fi

echo ""
echo "=========================================="
echo "检查完成，日志已保存: $LOG_FILE"
echo "=========================================="
