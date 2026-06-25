#!/bin/bash
# 完整的修复验证脚本

echo "========================================="
echo "🔍 路由系统修复验证"
echo "========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查函数
check_pass() {
    echo -e "${GREEN}✅ $1${NC}"
}

check_fail() {
    echo -e "${RED}❌ $1${NC}"
}

check_warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

# 1. 检查 PostgreSQL
echo "1️⃣  检查 PostgreSQL 服务..."
if brew services list | grep -q "postgresql.*started"; then
    check_pass "PostgreSQL 服务运行中"
else
    check_fail "PostgreSQL 服务未运行"
    echo "   运行: brew services start postgresql@15"
    exit 1
fi
echo ""

# 2. 检查数据库连接
echo "2️⃣  检查数据库连接..."
if psql -d llm_gateway -c "SELECT 1;" > /dev/null 2>&1; then
    check_pass "数据库 llm_gateway 连接成功"
else
    check_fail "无法连接到数据库 llm_gateway"
    exit 1
fi
echo ""

# 3. 检查视图存在
echo "3️⃣  检查视图存在..."
VIEW_EXISTS=$(psql -d llm_gateway -t -c "SELECT COUNT(*) FROM information_schema.views WHERE table_name = 'v_routable_credential_models';" 2>/dev/null | tr -d ' ')
if [ "$VIEW_EXISTS" = "1" ]; then
    check_pass "视图 v_routable_credential_models 存在"
else
    check_fail "视图 v_routable_credential_models 不存在"
    exit 1
fi
echo ""

# 4. 检查表结构
echo "4️⃣  检查表结构..."
TABLES=("providers" "credentials" "model_offers")
for table in "${TABLES[@]}"; do
    if psql -d llm_gateway -t -c "\dt $table" > /dev/null 2>&1; then
        check_pass "表 $table 存在"
    else
        check_fail "表 $table 不存在"
    fi
done
echo ""

# 5. 检查可路由节点
echo "5️⃣  检查可路由节点..."
ROUTABLE=$(psql -d llm_gateway -t -c "SELECT COUNT(*) FROM v_routable_credential_models WHERE tenant_id = 'default' AND is_routable = TRUE;" 2>/dev/null | tr -d ' ')
if [ "$ROUTABLE" -gt 0 ]; then
    check_pass "找到 $ROUTABLE 个可路由节点"
else
    check_warn "没有可路由节点（需要添加生产数据）"
fi
echo ""

# 6. 显示统计信息
echo "6️⃣  系统统计..."
psql -d llm_gateway -c "
SELECT 
    'Providers' as 类型, COUNT(*)::text as 数量 FROM providers
UNION ALL
SELECT 'Credentials', COUNT(*)::text FROM credentials
UNION ALL
SELECT 'Model Offers', COUNT(*)::text FROM model_offers
UNION ALL
SELECT 'Routable Nodes', COUNT(*)::text FROM v_routable_credential_models WHERE tenant_id = 'default' AND is_routable = TRUE;
" 2>/dev/null
echo ""

# 7. 检查创建的文件
echo "7️⃣  检查修复工具文件..."
FILES=(
    "fix_routing_issue.sql"
    "init_database.sql"
    "diagnose_and_fix.sh"
    "diagnose_routing.md"
    "README_FIX.md"
    "REPAIR_REPORT.md"
    "SUMMARY.md"
)
for file in "${FILES[@]}"; do
    if [ -f "$file" ]; then
        check_pass "文件 $file 存在"
    else
        check_warn "文件 $file 不存在"
    fi
done
echo ""

# 8. 最终状态
echo "========================================="
echo "📊 验证结果汇总"
echo "========================================="
if [ "$ROUTABLE" -gt 0 ]; then
    echo -e "${GREEN}✅ 系统状态: 正常${NC}"
    echo -e "${GREEN}✅ 可路由节点: $ROUTABLE 个${NC}"
    echo -e "${GREEN}✅ 修复状态: 完成${NC}"
    echo ""
    echo "🎉 路由系统已成功修复并可以正常工作！"
    echo ""
    echo "下一步操作："
    echo "1. 添加实际的 Provider 和 Credential 数据（参考 SUMMARY.md）"
    echo "2. 启动应用: go run ./cmd/gateway"
    echo "3. 测试路由: curl http://localhost:8080/api/routing/resolve?model=gpt-4"
else
    echo -e "${YELLOW}⚠️  系统状态: 部分完成${NC}"
    echo -e "${YELLOW}⚠️  可路由节点: 0 个（需要添加数据）${NC}"
    echo -e "${GREEN}✅ 修复状态: 基础结构完成${NC}"
    echo ""
    echo "需要添加实际的 Provider 和 Credential 数据才能使用。"
    echo "参考 SUMMARY.md 中的示例 SQL。"
fi
echo ""
echo "========================================="

exit 0
