#!/bin/bash
# 价格体系审计问题生产环境验证脚本
# 用途：验证P0问题的修复是否已部署到生产环境

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=================================================="
echo "价格体系审计问题生产环境验证"
echo "验证日期: $(date '+%Y-%m-%d %H:%M:%S')"
echo "=================================================="
echo ""

# 配置（根据实际环境修改）
DB_HOST="${DB_HOST:-172.16.2.211}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-acc_db}"
DB_USER="${DB_USER:-acc_super}"

# 验证1: 检查P0-1（缓存Token口径识别）修复是否生效
echo "🔍 验证1: P0-1 缓存Token口径识别修复"
echo "   期望: 2026-10-06之后的cache_read_tokens > prompt_tokens记录中，cost_usd应该有值"
echo ""

QUERY1="
SELECT 
    DATE_TRUNC('day', created_at) as date,
    COUNT(*) as total_cache_heavy_requests,
    COUNT(cost_usd) as has_cost,
    COUNT(*) - COUNT(cost_usd) as null_cost,
    ROUND(100.0 * COUNT(cost_usd) / NULLIF(COUNT(*), 0), 2) as pct_has_cost
FROM session_turns
WHERE created_at >= '2026-10-06'
  AND cache_read_tokens > prompt_tokens
GROUP BY DATE_TRUNC('day', created_at)
ORDER BY date DESC
LIMIT 7;
"

echo "执行查询..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "$QUERY1" 2>&1 | tee /tmp/p0-1-verify.txt

NULL_COST_COUNT=$(grep -oP '\d+(?=\s+\|\s+0\s*$)' /tmp/p0-1-verify.txt | head -1 || echo "0")

if [ "$NULL_COST_COUNT" = "0" ] || [ -z "$NULL_COST_COUNT" ]; then
    echo -e "${GREEN}✅ P0-1 验证通过：缓存Token成本计算正常${NC}"
else
    echo -e "${YELLOW}⚠️  P0-1 验证失败：仍有 $NULL_COST_COUNT 条记录的cost_usd为NULL${NC}"
    echo "   可能原因：修复代码尚未部署到生产环境"
fi
echo ""
echo "=================================================="
echo ""

# 验证2: 检查P0-2（成本→积分桥接）是否生效
echo "🔍 验证2: P0-2 成本→积分桥接"
echo "   期望: 非default租户的session_turns记录中，credits_charged应该有值"
echo ""

QUERY2="
SELECT 
    DATE_TRUNC('day', created_at) as date,
    COUNT(*) as total_requests,
    COUNT(credits_charged) as has_credits,
    COUNT(*) - COUNT(credits_charged) as null_credits,
    ROUND(100.0 * COUNT(credits_charged) / NULLIF(COUNT(*), 0), 2) as pct_has_credits
FROM session_turns
WHERE created_at >= CURRENT_DATE - INTERVAL '7 days'
  AND tenant_id != 'default'
  AND tenant_id IS NOT NULL
GROUP BY DATE_TRUNC('day', created_at)
ORDER BY date DESC;
"

echo "执行查询..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "$QUERY2" 2>&1 | tee /tmp/p0-2-verify.txt

# 检查最近一天的覆盖率
PCT_HAS_CREDITS=$(tail -1 /tmp/p0-2-verify.txt | grep -oP '\d+\.\d+(?=\s*$)' || echo "0")

if (( $(echo "$PCT_HAS_CREDITS > 80" | bc -l) )); then
    echo -e "${GREEN}✅ P0-2 验证通过：积分计费覆盖率 $PCT_HAS_CREDITS%${NC}"
elif (( $(echo "$PCT_HAS_CREDITS > 10" | bc -l) )); then
    echo -e "${YELLOW}⚠️  P0-2 部分生效：积分计费覆盖率 $PCT_HAS_CREDITS%${NC}"
    echo "   可能原因：修复代码最近才部署，或某些流量类型不计费"
else
    echo -e "${RED}❌ P0-2 验证失败：积分计费覆盖率仅 $PCT_HAS_CREDITS%${NC}"
    echo "   可能原因：成本→积分桥接代码尚未部署"
fi
echo ""
echo "=================================================="
echo ""

# 验证3: 检查当前部署版本
echo "🔍 验证3: 当前部署版本"
echo "   期望: git_sha 应该包含 P0 修复的提交"
echo ""

echo "检查数据库中记录的版本信息..."
QUERY3="
SELECT 
    'session_turns' as source,
    MAX(build_seq) as latest_build_seq,
    MAX(git_sha) as latest_git_sha
FROM (
    SELECT DISTINCT build_seq, git_sha 
    FROM session_turns 
    WHERE created_at >= CURRENT_DATE - INTERVAL '1 day'
) sub
UNION ALL
SELECT 
    'maas_settings' as source,
    NULL as latest_build_seq,
    NULL as latest_git_sha;
"

psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "$QUERY3"

echo ""
echo "关键修复的提交SHA："
echo "  P0-1 (Convention修复): d58a3c504d (2026-10-05)"
echo "  P0-2 (积分桥接): 多个提交，最早在2026-10之前"
echo ""
echo "如果生产版本的git_sha早于上述日期，说明修复尚未部署"
echo ""
echo "=================================================="
echo ""

# 验证4: 检查P1-3（多币种支持）
echo "🔍 验证4: P1-3 多币种使用情况"
echo "   检查: 生产环境实际使用了哪些币种"
echo ""

QUERY4="
SELECT 
    COALESCE(cost_currency, 'NULL') as currency,
    COUNT(*) as request_count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER(), 2) as percentage,
    SUM(cost_usd) as total_cost_usd
FROM session_turns
WHERE created_at >= CURRENT_DATE - INTERVAL '30 days'
  AND cost_usd IS NOT NULL
GROUP BY cost_currency
ORDER BY request_count DESC;
"

echo "执行查询..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "$QUERY4"

echo ""
echo "分析："
echo "  - 如果USD+CNY占比>95%，P1-3修复优先级可降低"
echo "  - 如果其他币种占比>5%，建议尽快实施P1-3修复"
echo ""
echo "=================================================="
echo ""

# 总结
echo "📊 验证总结"
echo ""
echo "请根据上述结果判断："
echo "1. 如果P0-1/P0-2验证失败 → 修复代码尚未部署，需要部署"
echo "2. 如果P0-1/P0-2验证通过 → 审计报告可能基于旧数据或旧代码版本"
echo "3. 根据P1-3的币种分布 → 决定是否需要实施多币种修复"
echo ""
echo "下一步建议："
echo "  □ 确认审计报告生成时使用的代码版本"
echo "  □ 如果修复未部署，安排部署包含修复的版本"
echo "  □ 根据P1-3验证结果，决定是否实施多币种修复"
echo ""
echo "=================================================="
echo "验证完成: $(date '+%Y-%m-%d %H:%M:%S')"
echo "=================================================="
