#!/bin/bash
# analyze-request-logs-size.sh
# 分析 request_logs 表的数据量分布

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 默认配置
DB_HOST="${DB_HOST:-172.31.0.4}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-stockuser}"
DB_PASSWORD="${DB_PASSWORD:-184_stock_pass_change_me}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

function log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

function log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

function log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

function log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

function run_sql() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -A "$@"
}

log_info "正在分析 request_logs 表数据量..."
echo ""

# 1. 总体统计
log_info "=== 总体统计 ==="
run_sql -c "
SELECT 
    '总行数: ' || COUNT(*)::text AS metric
FROM request_logs
UNION ALL
SELECT 
    '表大小: ' || pg_size_pretty(pg_total_relation_size('request_logs'))
FROM request_logs LIMIT 1
UNION ALL
SELECT 
    '索引大小: ' || pg_size_pretty(pg_indexes_size('request_logs'))
FROM request_logs LIMIT 1
UNION ALL
SELECT
    '数据大小: ' || pg_size_pretty(pg_relation_size('request_logs'))
FROM request_logs LIMIT 1;
"
echo ""

# 2. 按时间段统计
log_info "=== 按时间段统计 ==="
run_sql -c "
WITH stats AS (
    SELECT 
        COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '24 hours') AS last_24h,
        COUNT(*) FILTER (WHERE ts > NOW() - INTERVAL '7 days') AS last_7d,
        COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '30 days' AND NOW() - INTERVAL '7 days') AS warm_7_30d,
        COUNT(*) FILTER (WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days') AS cold_30_90d,
        COUNT(*) FILTER (WHERE ts < NOW() - INTERVAL '90 days') AS expired_90d,
        COUNT(*) AS total
    FROM request_logs
)
SELECT 
    '热数据 (最近7天): ' || last_7d || ' 行 (' || ROUND(last_7d::numeric / NULLIF(total, 0) * 100, 1) || '%)' AS stat
FROM stats
UNION ALL
SELECT 
    '  └─ 其中24小时: ' || last_24h || ' 行'
FROM stats
UNION ALL
SELECT 
    '温数据 (7-30天): ' || warm_7_30d || ' 行 (' || ROUND(warm_7_30d::numeric / NULLIF(total, 0) * 100, 1) || '%)'
FROM stats
UNION ALL
SELECT 
    '冷数据 (30-90天): ' || cold_30_90d || ' 行 (' || ROUND(cold_30_90d::numeric / NULLIF(total, 0) * 100, 1) || '%)'
FROM stats
UNION ALL
SELECT 
    '过期数据 (>90天): ' || expired_90d || ' 行 (' || ROUND(expired_90d::numeric / NULLIF(total, 0) * 100, 1) || '%)'
FROM stats;
"
echo ""

# 3. 按压缩策略统计
log_info "=== 按压缩策略统计 ==="
run_sql -c "
SELECT 
    COALESCE(NULLIF(compression_strategy, ''), 'none') AS strategy,
    COUNT(*) AS count,
    ROUND(COUNT(*)::numeric / SUM(COUNT(*)) OVER () * 100, 1) AS percentage
FROM request_logs
GROUP BY compression_strategy
ORDER BY count DESC;
" | awk -F'|' '{printf "  %-25s %10s 行 (%5s%%)\n", $1, $2, $3}'
echo ""

# 4. 按租户统计（前10）
log_info "=== 按租户统计 (Top 10) ==="
run_sql -c "
SELECT 
    COALESCE(tenant_id, 'default') AS tenant,
    COUNT(*) AS count,
    pg_size_pretty(SUM(LENGTH(COALESCE(request_body, ''::text)) + LENGTH(COALESCE(response_body, ''::text)))::bigint) AS body_size
FROM request_logs
GROUP BY tenant_id
ORDER BY count DESC
LIMIT 10;
" | awk -F'|' '{printf "  %-20s %10s 行  %10s\n", $1, $2, $3}'
echo ""

# 5. 增长趋势（最近7天每天）
log_info "=== 增长趋势 (最近7天) ==="
run_sql -c "
SELECT 
    DATE(ts) AS day,
    COUNT(*) AS requests,
    COUNT(*) FILTER (WHERE outbound_body IS NOT NULL) AS compressed,
    ROUND(COUNT(*) FILTER (WHERE outbound_body IS NOT NULL)::numeric / NULLIF(COUNT(*), 0) * 100, 1) AS compression_rate
FROM request_logs
WHERE ts > NOW() - INTERVAL '7 days'
GROUP BY DATE(ts)
ORDER BY day DESC;
" | awk -F'|' '{printf "  %s  %8s 请求  %8s 压缩 (%5s%%)\n", $1, $2, $3, $4}'
echo ""

# 6. 建议清理空间估算
log_info "=== 清理空间估算 ==="
COLD_COUNT=$(run_sql -c "SELECT COUNT(*) FROM request_logs WHERE ts BETWEEN NOW() - INTERVAL '90 days' AND NOW() - INTERVAL '30 days';")
EXPIRED_COUNT=$(run_sql -c "SELECT COUNT(*) FROM request_logs WHERE ts < NOW() - INTERVAL '90 days';")
TOTAL_SIZE=$(run_sql -c "SELECT pg_total_relation_size('request_logs');")

if [ "$COLD_COUNT" -gt 0 ]; then
    COLD_SIZE_EST=$(run_sql -c "SELECT pg_size_pretty((pg_total_relation_size('request_logs') * $COLD_COUNT::numeric / NULLIF(COUNT(*), 0))::bigint) FROM request_logs;")
    log_warn "归档冷数据 (30-90天)：$COLD_COUNT 行，预计释放 $COLD_SIZE_EST"
fi

if [ "$EXPIRED_COUNT" -gt 0 ]; then
    EXPIRED_SIZE_EST=$(run_sql -c "SELECT pg_size_pretty((pg_total_relation_size('request_logs') * $EXPIRED_COUNT::numeric / NULLIF(COUNT(*), 0))::bigint) FROM request_logs;")
    log_warn "删除过期数据 (>90天)：$EXPIRED_COUNT 行，预计释放 $EXPIRED_SIZE_EST"
fi

if [ "$COLD_COUNT" -eq 0 ] && [ "$EXPIRED_COUNT" -eq 0 ]; then
    log_success "当前无需清理数据"
fi

echo ""
log_success "分析完成"
