#!/bin/bash
# check-routing-mv-drift.sh
#
# 数据一致性巡检脚本：对比物化视图与基础视图的数据差异
# P2-D from handoff 20260901_011500
#
# 用途：
#   - 验证 routing_analytics_7d 与 request_logs 基础视图的一致性
#   - 检测数据漂移（drift > 5% 触发告警）
#   - 可配置 cron 每日运行
#
# 依赖：
#   - psql
#   - LLM_GATEWAY_DATABASE_URL 环境变量或参数传入
#
# 用法：
#   ./scripts/check-routing-mv-drift.sh [DATABASE_URL]
#   或设置环境变量：export LLM_GATEWAY_DATABASE_URL="postgresql://..."

set -euo pipefail

# 配置
DRIFT_THRESHOLD=5  # 允许的最大差异百分比
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_FILE="${SCRIPT_DIR}/../logs/mv-drift-check.log"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 日志函数
log() {
    local level="$1"
    shift
    local msg="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo -e "${timestamp} [${level}] ${msg}" | tee -a "${LOG_FILE}"
}

log_error() { log "${RED}ERROR${NC}" "$@"; }
log_warn() { log "${YELLOW}WARN${NC}" "$@"; }
log_info() { log "${GREEN}INFO${NC}" "$@"; }

# 获取数据库连接
DB_URL="${1:-${LLM_GATEWAY_DATABASE_URL:-}}"
if [ -z "$DB_URL" ]; then
    log_error "数据库连接未设置"
    echo "用法: $0 [DATABASE_URL]"
    echo "或设置环境变量: export LLM_GATEWAY_DATABASE_URL=\"postgresql://...\""
    exit 1
fi

# 确保日志目录存在
mkdir -p "$(dirname "$LOG_FILE")"

log_info "=== 开始物化视图数据一致性检查 ==="

# 1. 检查物化视图是否存在
log_info "检查物化视图是否存在..."
MV_EXISTS=$(psql "$DB_URL" -tAc "SELECT EXISTS (
    SELECT 1 FROM pg_matviews 
    WHERE schemaname = 'public' 
    AND matviewname = 'routing_analytics_7d'
)")

if [ "$MV_EXISTS" != "t" ]; then
    log_error "物化视图 routing_analytics_7d 不存在"
    exit 1
fi
log_info "✓ 物化视图存在"

# 2. 检查物化视图新鲜度
log_info "检查物化视图新鲜度..."
REFRESHED_AGE=$(psql "$DB_URL" -tAc "
    SELECT EXTRACT(EPOCH FROM (NOW() - MAX(refreshed_at)))::int 
    FROM routing_analytics_7d
")

if [ "$REFRESHED_AGE" -gt 900 ]; then  # 15分钟 = 900秒
    log_warn "物化视图已过期 (${REFRESHED_AGE}s > 900s)，数据可能不一致"
else
    log_info "✓ 物化视图新鲜 (${REFRESHED_AGE}s 前刷新)"
fi

# 3. 对比总行数：MV vs 基础视图
log_info "对比总请求数..."
COUNTS=$(psql "$DB_URL" -tAc "
SELECT 
    (SELECT SUM(request_count) FROM routing_analytics_7d) AS mv_total,
    (SELECT COUNT(*) FROM request_logs_with_current_month_without_customer_id
     WHERE ts >= NOW() - INTERVAL '7 days'
       AND (is_auto_request = TRUE
            OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''))) AS base_total
")

MV_TOTAL=$(echo "$COUNTS" | cut -d'|' -f1 | tr -d ' ')
BASE_TOTAL=$(echo "$COUNTS" | cut -d'|' -f2 | tr -d ' ')

log_info "物化视图总数: $MV_TOTAL"
log_info "基础视图总数: $BASE_TOTAL"

# 计算差异百分比
if [ "$BASE_TOTAL" -eq 0 ]; then
    log_warn "基础视图无数据，跳过差异检查"
    exit 0
fi

DIFF=$((BASE_TOTAL - MV_TOTAL))
DIFF_PCT=$(echo "scale=2; ($DIFF * 100) / $BASE_TOTAL" | bc)
DIFF_PCT_ABS=$(echo "$DIFF_PCT" | tr -d '-')

log_info "差异: $DIFF 行 (${DIFF_PCT}%)"

# 4. 判断是否超过阈值
if (( $(echo "$DIFF_PCT_ABS > $DRIFT_THRESHOLD" | bc -l) )); then
    log_error "⚠️  数据漂移超过阈值！"
    log_error "   差异: ${DIFF_PCT}% (阈值: ${DRIFT_THRESHOLD}%)"
    log_error "   MV 总数: $MV_TOTAL"
    log_error "   Base 总数: $BASE_TOTAL"
    log_error "   差值: $DIFF 行"
    
    # 5. 发送告警（如果配置了 Lark webhook）
    if [ -n "${LARK_WEBHOOK_URL:-}" ]; then
        ALERT_MSG="物化视图数据漂移告警\n差异: ${DIFF_PCT}% (阈值: ${DRIFT_THRESHOLD}%)\nMV: $MV_TOTAL | Base: $BASE_TOTAL | Diff: $DIFF"
        curl -s -X POST "$LARK_WEBHOOK_URL" \
            -H "Content-Type: application/json" \
            -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"$ALERT_MSG\"}}" \
            > /dev/null || log_warn "发送 Lark 告警失败"
    fi
    
    exit 1
else
    log_info "✓ 数据一致性检查通过 (差异 ${DIFF_PCT}% ≤ ${DRIFT_THRESHOLD}%)"
fi

# 6. 详细对比：按 task_type 分组统计
log_info "详细对比（按 task_type）..."
psql "$DB_URL" -c "
WITH mv_stats AS (
    SELECT 
        effective_task_type,
        SUM(request_count) AS mv_count
    FROM routing_analytics_7d
    GROUP BY effective_task_type
),
base_stats AS (
    SELECT 
        COALESCE(
            NULLIF(task_type, ''),
            CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END
        ) AS effective_task_type,
        COUNT(*) AS base_count
    FROM request_logs_with_current_month_without_customer_id
    WHERE ts >= NOW() - INTERVAL '7 days'
      AND (is_auto_request = TRUE
           OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> ''))
    GROUP BY effective_task_type
)
SELECT 
    COALESCE(m.effective_task_type, b.effective_task_type) AS task_type,
    COALESCE(m.mv_count, 0) AS mv_count,
    COALESCE(b.base_count, 0) AS base_count,
    COALESCE(b.base_count, 0) - COALESCE(m.mv_count, 0) AS diff,
    ROUND(
        CASE 
            WHEN COALESCE(b.base_count, 0) = 0 THEN 0
            ELSE ((COALESCE(b.base_count, 0) - COALESCE(m.mv_count, 0))::numeric * 100) / b.base_count
        END, 
        2
    ) AS diff_pct
FROM mv_stats m
FULL OUTER JOIN base_stats b ON m.effective_task_type = b.effective_task_type
ORDER BY ABS(COALESCE(b.base_count, 0) - COALESCE(m.mv_count, 0)) DESC
LIMIT 10;
" | tee -a "${LOG_FILE}"

log_info "=== 检查完成 ==="
exit 0
