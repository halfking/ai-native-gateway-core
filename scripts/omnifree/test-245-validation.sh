#!/bin/bash
# OmniFree 245 环境测试验证脚本
# 用途：全面测试部署后的功能

set -e

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${BLUE}ℹ️  $1${NC}"; }
success() { echo -e "${GREEN}✅ $1${NC}"; }
warn() { echo -e "${YELLOW}⚠️  $1${NC}"; }
error() { echo -e "${RED}❌ $1${NC}"; }

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
info "OmniFree 245 环境测试验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 检查环境变量
if [ -z "$OMNIFREE_DB_URL" ]; then
    error "请设置环境变量 OMNIFREE_DB_URL"
    exit 1
fi

DB_URL="$OMNIFREE_DB_URL"

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0
WARNINGS=0

run_test() {
    local test_name="$1"
    local expected="$2"
    local actual="$3"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    
    if [ "$actual" = "$expected" ]; then
        success "$test_name: $actual (通过)"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        return 0
    else
        error "$test_name: 期望 $expected, 实际 $actual (失败)"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        return 1
    fi
}

# ============================================================================
# 测试 1: 数据完整性
# ============================================================================

echo ""
info "测试 1/6: 数据完整性检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 检查表是否存在
info "检查表是否创建..."
TABLE_COUNT=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_schema = 'public' 
  AND table_name IN ('free_resource_catalog', 'free_quota_tracker', 
                      'auto_combo_templates', 'keyless_providers');
")
run_test "表数量" "4" "$TABLE_COUNT"

# 检查记录数
info "检查记录数量..."
RESOURCE_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM free_resource_catalog WHERE tenant_id='default';")
TEMPLATE_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM auto_combo_templates WHERE tenant_id='default';")
KEYLESS_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM keyless_providers WHERE tenant_id='default';")

run_test "免费资源数量" "15" "$RESOURCE_COUNT"
run_test "Auto Combo 模板数量" "6" "$TEMPLATE_COUNT"
run_test "Keyless 提供商数量" "3" "$KEYLESS_COUNT"

# ============================================================================
# 测试 2: RLS 策略
# ============================================================================

echo ""
info "测试 2/6: RLS 策略验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 检查 RLS 是否启用
info "检查 RLS 启用状态..."
RLS_ENABLED=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relname IN ('free_resource_catalog', 'free_quota_tracker', 
                     'auto_combo_templates', 'keyless_providers')
  AND c.relrowsecurity = true;
")
run_test "RLS 启用表数量" "4" "$RLS_ENABLED"

# 检查策略数量
POLICY_COUNT=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM pg_policies 
WHERE schemaname = 'public'
  AND tablename IN ('free_resource_catalog', 'free_quota_tracker', 
                     'auto_combo_templates', 'keyless_providers');
")
info "RLS 策略数量: $POLICY_COUNT"
if [ "$POLICY_COUNT" -lt 4 ]; then
    warn "RLS 策略数量少于预期（应至少 4 个）"
    WARNINGS=$((WARNINGS + 1))
fi

# ============================================================================
# 测试 3: 配额总量
# ============================================================================

echo ""
info "测试 3/6: 配额总量验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

MONTHLY_GB=$(psql "$DB_URL" -tAc "
SELECT ROUND(SUM(monthly_tokens)::numeric / 1000000000, 2)
FROM free_resource_catalog 
WHERE enabled = TRUE AND tenant_id = 'default';
")

info "月度配额总量: ${MONTHLY_GB}B tokens"
# 预期约 1.18B
EXPECTED_MIN=1.0
EXPECTED_MAX=1.5
if (( $(echo "$MONTHLY_GB >= $EXPECTED_MIN" | bc -l) )) && (( $(echo "$MONTHLY_GB <= $EXPECTED_MAX" | bc -l) )); then
    success "配额总量在预期范围内 (1.0-1.5B)"
    PASSED_TESTS=$((PASSED_TESTS + 1))
else
    warn "配额总量超出预期范围: ${MONTHLY_GB}B (预期 1.0-1.5B)"
    WARNINGS=$((WARNINGS + 1))
fi
TOTAL_TESTS=$((TOTAL_TESTS + 1))

# ToS 分布
TOS_OK=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM free_resource_catalog 
WHERE tos_verdict='ok' AND enabled=TRUE AND tenant_id='default';
")
TOS_CAUTION=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM free_resource_catalog 
WHERE tos_verdict='caution' AND enabled=TRUE AND tenant_id='default';
")
TOS_AVOID=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM free_resource_catalog 
WHERE tos_verdict='avoid' AND enabled=TRUE AND tenant_id='default';
")

info "ToS 分布: OK=$TOS_OK, Caution=$TOS_CAUTION, Avoid=$TOS_AVOID"

# ============================================================================
# 测试 4: 查询性能
# ============================================================================

echo ""
info "测试 4/6: 查询性能测试"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 测试简单查询
info "测试资源列表查询..."
START_TIME=$(date +%s%3N)
psql "$DB_URL" -c "
SELECT provider_code, model_id, free_type, monthly_tokens
FROM free_resource_catalog
WHERE tenant_id = 'default' AND enabled = TRUE
ORDER BY monthly_tokens DESC
LIMIT 10;
" > /dev/null
END_TIME=$(date +%s%3N)
QUERY_TIME=$((END_TIME - START_TIME))

info "查询耗时: ${QUERY_TIME}ms"
if [ "$QUERY_TIME" -lt 100 ]; then
    success "查询性能优秀 (<100ms)"
    PASSED_TESTS=$((PASSED_TESTS + 1))
elif [ "$QUERY_TIME" -lt 500 ]; then
    warn "查询性能一般 (100-500ms)"
    WARNINGS=$((WARNINGS + 1))
    PASSED_TESTS=$((PASSED_TESTS + 1))
else
    error "查询性能较差 (>500ms)"
    FAILED_TESTS=$((FAILED_TESTS + 1))
fi
TOTAL_TESTS=$((TOTAL_TESTS + 1))

# 测试模板查询
info "测试模板查询..."
START_TIME=$(date +%s%3N)
psql "$DB_URL" -c "
SELECT combo_name, variant, max_candidates
FROM auto_combo_templates
WHERE tenant_id = 'default' AND enabled = TRUE;
" > /dev/null
END_TIME=$(date +%s%3N)
TEMPLATE_QUERY_TIME=$((END_TIME - START_TIME))

info "模板查询耗时: ${TEMPLATE_QUERY_TIME}ms"

# ============================================================================
# 测试 5: 租户隔离
# ============================================================================

echo ""
info "测试 5/6: 租户隔离测试"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 测试 default 租户
DEFAULT_COUNT=$(psql "$DB_URL" -tAc "
SET app.current_tenant = 'default';
SELECT COUNT(*) FROM free_resource_catalog;
")
info "default 租户资源数: $DEFAULT_COUNT"

# 测试其他租户（应该看不到 default 的数据）
OTHER_TENANT_COUNT=$(psql "$DB_URL" -tAc "
SET app.current_tenant = 'test-tenant-999';
SELECT COUNT(*) FROM free_resource_catalog;
")
info "test-tenant-999 租户资源数: $OTHER_TENANT_COUNT"

if [ "$OTHER_TENANT_COUNT" = "0" ]; then
    success "租户隔离有效（其他租户看不到 default 数据）"
    PASSED_TESTS=$((PASSED_TESTS + 1))
else
    warn "租户隔离可能未生效（其他租户看到 $OTHER_TENANT_COUNT 条记录）"
    WARNINGS=$((WARNINGS + 1))
fi
TOTAL_TESTS=$((TOTAL_TESTS + 1))

# ============================================================================
# 测试 6: 索引和约束
# ============================================================================

echo ""
info "测试 6/6: 索引和约束验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 检查索引数量
INDEX_COUNT=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM pg_indexes 
WHERE schemaname = 'public'
  AND tablename IN ('free_resource_catalog', 'free_quota_tracker', 
                     'auto_combo_templates', 'keyless_providers');
")
info "索引数量: $INDEX_COUNT"
if [ "$INDEX_COUNT" -lt 15 ]; then
    warn "索引数量少于预期（应至少 15 个）"
    WARNINGS=$((WARNINGS + 1))
fi

# 检查唯一约束
CONSTRAINT_COUNT=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public'
  AND t.relname IN ('free_resource_catalog', 'auto_combo_templates', 'keyless_providers')
  AND c.contype = 'u';
")
info "唯一约束数量: $CONSTRAINT_COUNT"

# ============================================================================
# 详细数据检查
# ============================================================================

echo ""
info "详细数据检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 显示前 5 个免费资源
info "前 5 个免费资源 (按月度配额排序):"
psql "$DB_URL" -c "
SELECT 
    provider_code,
    model_id,
    free_type,
    ROUND(monthly_tokens::numeric / 1000000, 0) AS monthly_m,
    tos_verdict,
    enabled
FROM free_resource_catalog
WHERE tenant_id = 'default'
ORDER BY monthly_tokens DESC
LIMIT 5;
"

# 显示所有模板
info "可用的 Auto Combo 模板:"
psql "$DB_URL" -c "
SELECT 
    combo_name,
    variant,
    max_candidates,
    enabled
FROM auto_combo_templates
WHERE tenant_id = 'default'
ORDER BY combo_name;
"

# ============================================================================
# 测试总结
# ============================================================================

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
info "测试总结"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

info "测试结果:"
echo "  总计: $TOTAL_TESTS 个测试"
echo "  通过: $PASSED_TESTS 个"
echo "  失败: $FAILED_TESTS 个"
echo "  警告: $WARNINGS 个"
echo ""

PASS_RATE=$((PASSED_TESTS * 100 / TOTAL_TESTS))
echo "  通过率: ${PASS_RATE}%"
echo ""

if [ "$FAILED_TESTS" -eq 0 ]; then
    if [ "$WARNINGS" -eq 0 ]; then
        success "🎉 所有测试通过！数据层部署成功！"
        echo ""
        info "下一步:"
        echo "  1. 数据层验证完成，可以开始应用层集成"
        echo "  2. 按 docs/omnifree/HANDOFF.md 实施 Phase 1-4"
        echo "  3. 预计 2-3 天完成应用层集成"
        exit 0
    else
        warn "⚠️  所有测试通过，但有 $WARNINGS 个警告"
        echo ""
        info "建议:"
        echo "  1. 检查警告信息并评估影响"
        echo "  2. 如果警告可接受，可以继续应用层集成"
        exit 0
    fi
else
    error "❌ 有 $FAILED_TESTS 个测试失败"
    echo ""
    info "建议:"
    echo "  1. 检查上述失败的测试"
    echo "  2. 查看错误信息并修复问题"
    echo "  3. 重新运行测试验证"
    exit 1
fi
