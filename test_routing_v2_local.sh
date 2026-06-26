#!/bin/bash
# ============================================================================
# routing-v2 统计功能本地测试脚本
# ============================================================================
# 用途：在本地环境测试 routing-v2 统计功能是否正常工作
# 使用：./test_routing_v2_local.sh

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=========================================${NC}"
echo -e "${BLUE}routing-v2 统计功能本地测试${NC}"
echo -e "${BLUE}=========================================${NC}"
echo ""

# 检查环境变量
if [ -z "$DATABASE_URL" ]; then
    echo -e "${RED}错误：DATABASE_URL 环境变量未设置${NC}"
    echo "请设置 DATABASE_URL，例如："
    echo "export DATABASE_URL='postgresql://user:pass@localhost:5432/dbname'"
    exit 1
fi

echo -e "${GREEN}✓ DATABASE_URL 已设置${NC}"
echo ""

# 第一步：运行 Go 单元测试
echo -e "${BLUE}[1/5] 运行 Go 单元测试...${NC}"
if go test ./admin -v -run TestSpecified 2>&1 | tee /tmp/test_output.log | grep -q "PASS"; then
    echo -e "${GREEN}✓ Go 单元测试通过${NC}"
else
    echo -e "${RED}✗ Go 单元测试失败${NC}"
    echo "详细日志："
    cat /tmp/test_output.log
    exit 1
fi
echo ""

# 第二步：检查数据库连接
echo -e "${BLUE}[2/5] 检查数据库连接...${NC}"
if psql "$DATABASE_URL" -c "SELECT 1" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 数据库连接正常${NC}"
else
    echo -e "${RED}✗ 数据库连接失败${NC}"
    exit 1
fi
echo ""

# 第三步：运行诊断脚本
echo -e "${BLUE}[3/5] 运行数据库诊断...${NC}"
if [ -f "diagnose_routing_v2_stats.sql" ]; then
    psql "$DATABASE_URL" -f diagnose_routing_v2_stats.sql > /tmp/diagnose_output.log 2>&1
    echo -e "${GREEN}✓ 诊断脚本执行完成${NC}"
    echo "诊断结果已保存到：/tmp/diagnose_output.log"
    
    # 检查关键指标
    echo ""
    echo -e "${YELLOW}关键指标摘要：${NC}"
    grep -A 1 "total_requests\|total_auto\|total_specified\|success_rate_pct" /tmp/diagnose_output.log | head -20
else
    echo -e "${YELLOW}⚠ 诊断脚本不存在，跳过${NC}"
fi
echo ""

# 第四步：检查索引
echo -e "${BLUE}[4/5] 检查必需索引...${NC}"
INDEX_CHECK=$(psql "$DATABASE_URL" -t -c "
SELECT COUNT(*)
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND indexname = 'idx_request_logs_explicit_model';
")

if [ "$INDEX_CHECK" -eq "1" ]; then
    echo -e "${GREEN}✓ idx_request_logs_explicit_model 索引存在${NC}"
else
    echo -e "${YELLOW}⚠ idx_request_logs_explicit_model 索引不存在${NC}"
    echo "建议运行：psql \$DATABASE_URL < docs/2026-06-22-explicit-model-stats.sql"
fi
echo ""

# 第五步：测试统计查询
echo -e "${BLUE}[5/5] 测试统计查询...${NC}"
STATS_RESULT=$(psql "$DATABASE_URL" -t -c "
SELECT 
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_count,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as explicit_count
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  );
")

echo "7天统计结果："
echo "$STATS_RESULT"

TOTAL=$(echo "$STATS_RESULT" | awk '{print $1}')
if [ "$TOTAL" -gt "0" ]; then
    echo -e "${GREEN}✓ 统计查询返回数据 ($TOTAL 条记录)${NC}"
else
    echo -e "${YELLOW}⚠ 统计查询返回空数据${NC}"
    echo "这可能意味着："
    echo "  - 最近 7 天没有请求数据"
    echo "  - request_logs 写入有问题"
    echo "  - 数据不符合查询条件"
fi
echo ""

# 总结
echo -e "${BLUE}=========================================${NC}"
echo -e "${BLUE}测试完成！${NC}"
echo -e "${BLUE}=========================================${NC}"
echo ""
echo "详细报告："
echo "  - Go 测试日志：/tmp/test_output.log"
echo "  - 诊断结果：/tmp/diagnose_output.log"
echo ""
echo "下一步："
echo "  1. 查看诊断结果：cat /tmp/diagnose_output.log"
echo "  2. 如果有数据但无统计，检查查询条件是否匹配"
echo "  3. 如果完全无数据，检查 telemetry 写入是否正常"
echo "  4. 如果索引缺失，运行索引创建脚本"
echo ""
