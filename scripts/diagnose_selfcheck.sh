#!/bin/bash

# =====================================================================
# 自检系统诊断脚本
# 生成日期: 2026-09-06
# 用途: 快速诊断节点状态同步问题，生成诊断报告
# =====================================================================

set -euo pipefail

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-postgres}"
OUTPUT_DIR="./diagnostics_reports"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_FILE="${OUTPUT_DIR}/selfcheck_diagnostic_${TIMESTAMP}.txt"

# 创建输出目录
mkdir -p "${OUTPUT_DIR}"

# 打印带颜色的消息
print_header() {
    echo -e "\n${BLUE}================================================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}================================================================${NC}\n"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# 执行 SQL 查询
run_query() {
    local query="$1"
    local description="$2"
    
    echo "执行: $description" | tee -a "$REPORT_FILE"
    echo "---" | tee -a "$REPORT_FILE"
    
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -c "$query" 2>&1 | tee -a "$REPORT_FILE"
    
    echo "" | tee -a "$REPORT_FILE"
}

# 开始诊断
print_header "自检系统诊断工具"
echo "数据库: ${DB_HOST}:${DB_PORT}/${DB_NAME}" | tee "$REPORT_FILE"
echo "时间: $(date)" | tee -a "$REPORT_FILE"
echo "" | tee -a "$REPORT_FILE"

# 检查数据库连接
print_header "1. 检查数据库连接"
if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" >/dev/null 2>&1; then
    print_success "数据库连接成功"
else
    print_error "数据库连接失败，请检查配置"
    exit 1
fi

# 诊断 1: 模型绑定歧义
print_header "2. 诊断模型绑定歧义"
AMBIGUOUS_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "
SELECT COUNT(*) FROM (
    SELECT c.id
    FROM credentials c
    JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
    JOIN provider_models pm ON cmb.provider_model_id = pm.id
    WHERE c.status = 'active' AND c.manual_disabled = FALSE
    GROUP BY c.id, pm.standardized_name
    HAVING COUNT(DISTINCT pm.raw_model_name) > 1
) sub;
" | tr -d ' ')

if [ "$AMBIGUOUS_COUNT" -eq 0 ]; then
    print_success "未发现模型绑定歧义"
else
    print_warning "发现 $AMBIGUOUS_COUNT 个凭据存在模型绑定歧义"
    run_query "
    SELECT 
        c.id AS credential_id,
        c.label AS credential_label,
        pv.display_name AS provider_name,
        pm.standardized_name,
        STRING_AGG(pm.raw_model_name, ', ' ORDER BY pm.raw_model_name) AS ambiguous_models,
        COUNT(DISTINCT pm.raw_model_name) AS model_count
    FROM credentials c
    JOIN providers pv ON pv.id = c.provider_id
    JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
    JOIN provider_models pm ON cmb.provider_model_id = pm.id
    WHERE c.status = 'active' AND c.manual_disabled = FALSE
    GROUP BY c.id, c.label, pv.display_name, pm.standardized_name
    HAVING COUNT(DISTINCT pm.raw_model_name) > 1
    ORDER BY model_count DESC
    LIMIT 20;
    " "模型绑定歧义详情（前20个）"
fi

# 诊断 2: NULL unavailable_recover_at
print_header "3. 诊断 NULL unavailable_recover_at"
NULL_RECOVER_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "
SELECT COUNT(*) 
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND cmb.unavailable_recover_at IS NULL
  AND c.status = 'active';
" | tr -d ' ')

if [ "$NULL_RECOVER_COUNT" -eq 0 ]; then
    print_success "未发现 NULL unavailable_recover_at"
else
    print_error "发现 $NULL_RECOVER_COUNT 个绑定的 unavailable_recover_at 为 NULL"
    run_query "
    SELECT 
        cmb.credential_id,
        c.label AS credential_label,
        pm.raw_model_name,
        cmb.unavailable_reason,
        cmb.unavailable_at,
        EXTRACT(EPOCH FROM (NOW() - cmb.unavailable_at))/60 AS unavailable_minutes
    FROM credential_model_bindings cmb
    JOIN credentials c ON c.id = cmb.credential_id
    JOIN provider_models pm ON pm.id = cmb.provider_model_id
    WHERE cmb.available = FALSE
      AND cmb.unavailable_reason NOT LIKE 'manual%'
      AND cmb.unavailable_recover_at IS NULL
      AND c.status = 'active'
    ORDER BY cmb.unavailable_at ASC
    LIMIT 20;
    " "NULL unavailable_recover_at 详情（前20个）"
fi

# 诊断 3: 过期但未恢复的凭据
print_header "4. 诊断过期但未恢复的凭据"
OVERDUE_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "
SELECT COUNT(*)
FROM credentials c
WHERE c.availability_state IN ('cooling', 'rate_limited', 'unreachable', 'suspended')
  AND c.availability_recover_at IS NOT NULL
  AND c.availability_recover_at < NOW() - INTERVAL '10 minutes'
  AND c.status = 'active';
" | tr -d ' ')

if [ "$OVERDUE_COUNT" -eq 0 ]; then
    print_success "未发现过期未恢复的凭据"
else
    print_warning "发现 $OVERDUE_COUNT 个凭据过期超过10分钟未恢复"
    run_query "
    SELECT 
        c.id AS credential_id,
        c.label,
        c.availability_state,
        c.availability_recover_at,
        EXTRACT(EPOCH FROM (NOW() - c.availability_recover_at))/60 AS overdue_minutes,
        c.state_reason_code
    FROM credentials c
    WHERE c.availability_state IN ('cooling', 'rate_limited', 'unreachable', 'suspended')
      AND c.availability_recover_at IS NOT NULL
      AND c.availability_recover_at < NOW() - INTERVAL '10 minutes'
      AND c.status = 'active'
    ORDER BY overdue_minutes DESC
    LIMIT 20;
    " "过期未恢复凭据详情（前20个）"
fi

# 诊断 4: 缺失 node_probe_state
print_header "5. 诊断缺失 node_probe_state"
MISSING_PROBE_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "
SELECT COUNT(*)
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
  AND cmb.unavailable_reason NOT LIKE 'manual%'
  AND nps.credential_id IS NULL
  AND c.status = 'active';
" | tr -d ' ')

if [ "$MISSING_PROBE_COUNT" -eq 0 ]; then
    print_success "所有不可用绑定都有 node_probe_state"
else
    print_warning "发现 $MISSING_PROBE_COUNT 个不可用绑定缺失 node_probe_state"
    run_query "
    SELECT 
        c.id AS credential_id,
        c.label,
        pm.raw_model_name,
        cmb.unavailable_reason,
        EXTRACT(EPOCH FROM (NOW() - cmb.unavailable_at))/60 AS unavailable_minutes
    FROM credentials c
    JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
    JOIN provider_models pm ON cmb.provider_model_id = pm.id
    LEFT JOIN node_probe_state nps 
        ON cmb.credential_id = nps.credential_id 
        AND pm.raw_model_name = nps.raw_model_name
    WHERE cmb.available = FALSE
      AND cmb.unavailable_reason NOT LIKE 'manual%'
      AND nps.credential_id IS NULL
      AND c.status = 'active'
    ORDER BY cmb.unavailable_at ASC
    LIMIT 20;
    " "缺失 node_probe_state 详情（前20个）"
fi

# 诊断 5: 长时间未执行的探测
print_header "6. 诊断长时间未执行的探测"
OVERDUE_PROBE_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -t -c "
SELECT COUNT(*)
FROM node_probe_state nps
JOIN credentials c ON c.id = nps.credential_id
WHERE nps.next_retry_at < NOW() - INTERVAL '5 minutes'
  AND nps.paused = FALSE
  AND (nps.in_flight_until IS NULL OR nps.in_flight_until < NOW())
  AND c.status = 'active';
" | tr -d ' ')

if [ "$OVERDUE_PROBE_COUNT" -eq 0 ]; then
    print_success "未发现延迟的探测任务"
else
    print_warning "发现 $OVERDUE_PROBE_COUNT 个探测任务延迟超过5分钟"
    run_query "
    SELECT 
        nps.credential_id,
        c.label,
        nps.raw_model_name,
        EXTRACT(EPOCH FROM (NOW() - nps.next_retry_at))/60 AS overdue_minutes,
        nps.consecutive_failures,
        nps.last_err_code
    FROM node_probe_state nps
    JOIN credentials c ON c.id = nps.credential_id
    WHERE nps.next_retry_at < NOW() - INTERVAL '5 minutes'
      AND nps.paused = FALSE
      AND (nps.in_flight_until IS NULL OR nps.in_flight_until < NOW())
      AND c.status = 'active'
    ORDER BY overdue_minutes DESC
    LIMIT 20;
    " "延迟探测任务详情（前20个）"
fi

# 统计摘要
print_header "7. 系统健康状况摘要"
run_query "
SELECT 
    'credentials_by_availability' AS metric,
    c.availability_state AS state,
    COUNT(*) AS count
FROM credentials c
WHERE c.status = 'active'
GROUP BY c.availability_state
ORDER BY count DESC;
" "凭据可用性状态分布"

run_query "
SELECT 
    'bindings_by_availability' AS metric,
    CASE WHEN cmb.available THEN 'available' ELSE 'unavailable' END AS state,
    COUNT(*) AS count
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
WHERE c.status = 'active'
GROUP BY cmb.available;
" "绑定可用性分布"

# 生成摘要报告
print_header "诊断摘要"
echo "诊断时间: $(date)" | tee -a "$REPORT_FILE"
echo "" | tee -a "$REPORT_FILE"

TOTAL_ISSUES=0

if [ "$AMBIGUOUS_COUNT" -gt 0 ]; then
    echo "⚠ 模型绑定歧义: $AMBIGUOUS_COUNT 个凭据" | tee -a "$REPORT_FILE"
    TOTAL_ISSUES=$((TOTAL_ISSUES + AMBIGUOUS_COUNT))
fi

if [ "$NULL_RECOVER_COUNT" -gt 0 ]; then
    echo "✗ NULL unavailable_recover_at: $NULL_RECOVER_COUNT 个绑定" | tee -a "$REPORT_FILE"
    TOTAL_ISSUES=$((TOTAL_ISSUES + NULL_RECOVER_COUNT))
fi

if [ "$OVERDUE_COUNT" -gt 0 ]; then
    echo "⚠ 过期未恢复凭据: $OVERDUE_COUNT 个" | tee -a "$REPORT_FILE"
    TOTAL_ISSUES=$((TOTAL_ISSUES + OVERDUE_COUNT))
fi

if [ "$MISSING_PROBE_COUNT" -gt 0 ]; then
    echo "⚠ 缺失 node_probe_state: $MISSING_PROBE_COUNT 个绑定" | tee -a "$REPORT_FILE"
    TOTAL_ISSUES=$((TOTAL_ISSUES + MISSING_PROBE_COUNT))
fi

if [ "$OVERDUE_PROBE_COUNT" -gt 0 ]; then
    echo "⚠ 延迟探测任务: $OVERDUE_PROBE_COUNT 个" | tee -a "$REPORT_FILE"
    TOTAL_ISSUES=$((TOTAL_ISSUES + OVERDUE_PROBE_COUNT))
fi

echo "" | tee -a "$REPORT_FILE"

if [ "$TOTAL_ISSUES" -eq 0 ]; then
    print_success "系统健康状况良好，未发现问题"
else
    print_warning "发现 $TOTAL_ISSUES 个需要关注的问题"
    echo "" | tee -a "$REPORT_FILE"
    echo "建议:" | tee -a "$REPORT_FILE"
    echo "1. 查看完整报告: $REPORT_FILE" | tee -a "$REPORT_FILE"
    echo "2. 参考修复 SQL: sql/diagnostics/selfcheck_diagnostics.sql" | tee -a "$REPORT_FILE"
    echo "3. 如需修复，建议先在测试环境验证" | tee -a "$REPORT_FILE"
fi

print_header "诊断完成"
echo "完整报告已保存到: $REPORT_FILE"
echo ""
echo "下一步操作:"
echo "1. 查看报告: cat $REPORT_FILE"
echo "2. 如发现问题，参考 sql/diagnostics/selfcheck_diagnostics.sql 中的修复 SQL"
echo "3. 定期运行此脚本监控系统健康状况"
