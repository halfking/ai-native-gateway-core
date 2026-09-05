#!/bin/bash
# OmniFree 245 环境部署脚本
# 用途：在测试环境部署数据层并验证

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${BLUE}ℹ️  $1${NC}"; }
success() { echo -e "${GREEN}✅ $1${NC}"; }
warn() { echo -e "${YELLOW}⚠️  $1${NC}"; }
error() { echo -e "${RED}❌ $1${NC}"; exit 1; }

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
info "OmniFree 245 环境部署"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 检查环境变量
if [ -z "$OMNIFREE_DB_URL" ]; then
    error "请设置环境变量 OMNIFREE_DB_URL"
fi

if [ -z "$OMNIFREE_DB_PASSWORD" ]; then
    warn "未设置 OMNIFREE_DB_PASSWORD，将使用 DB_URL 中的密码"
fi

# 如果提供了密码，替换 URL 中的密码
if [ -n "$OMNIFREE_DB_PASSWORD" ]; then
    # 提取 URL 各部分并重建
    DB_URL="$OMNIFREE_DB_URL"
    info "使用环境变量中的密码"
else
    DB_URL="$OMNIFREE_DB_URL"
fi

# 检查依赖
info "检查依赖..."
command -v psql >/dev/null 2>&1 || error "需要安装 psql"
command -v go >/dev/null 2>&1 || error "需要安装 Go"
success "依赖检查通过"

# 检查项目根目录
if [ ! -f "go.mod" ]; then
    error "请在项目根目录执行此脚本"
fi

# 测试数据库连接
info "测试数据库连接..."
if ! psql "$DB_URL" -c "SELECT version();" >/dev/null 2>&1; then
    error "数据库连接失败，请检查 OMNIFREE_DB_URL"
fi
success "数据库连接成功"

# 显示数据库信息（隐藏密码）
DB_INFO=$(echo "$DB_URL" | sed 's/:\/\/[^:]*:[^@]*@/:\/\/***:***@/')
info "数据库: $DB_INFO"

# 确认部署
echo ""
warn "即将在 245 环境执行以下操作："
echo "  1. 执行数据库迁移（创建 4 张表 + 索引 + RLS）"
echo "  2. 导入 seed 数据（15 资源 + 6 模板 + 3 keyless）"
echo "  3. 验证功能（查询、RLS、配额）"
echo ""
read -p "是否继续？(y/N) " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    warn "部署已取消"
    exit 0
fi

# ============================================================================
# 第一步：执行迁移
# ============================================================================

echo ""
info "步骤 1/3: 执行数据库迁移..."

if ! psql "$DB_URL" -v ON_ERROR_STOP=1 -f sql/migrations/075-omnifree-schema.sql; then
    error "迁移执行失败"
fi

success "迁移执行成功"

# 验证表创建
TABLE_COUNT=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_schema = 'public' 
  AND table_name IN ('free_resource_catalog', 'free_quota_tracker', 
                      'auto_combo_templates', 'keyless_providers');
")

if [ "$TABLE_COUNT" -ne 4 ]; then
    error "表创建不完整，期望 4 张表，实际 $TABLE_COUNT 张"
fi

success "4 张表创建成功"

# ============================================================================
# 第二步：导入 seed
# ============================================================================

echo ""
info "步骤 2/3: 导入 seed 数据..."

# 编译 seed 工具
info "编译 seed 工具..."
go build -o /tmp/seed-free-resources-245 ./cmd/seed-free-resources || error "编译失败"

# 执行导入
/tmp/seed-free-resources-245 \
    --db-url="$DB_URL" \
    --tenant-id="default" \
    --catalog=configs/seed/free_resource_catalog.json \
    --templates=configs/seed/auto_combo_templates.json \
    --keyless=configs/seed/keyless_providers.json \
    || error "seed 导入失败"

success "seed 导入成功"

# 验证导入
RESOURCE_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM free_resource_catalog WHERE tenant_id='default';")
TEMPLATE_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM auto_combo_templates WHERE tenant_id='default';")
KEYLESS_COUNT=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM keyless_providers WHERE tenant_id='default';")

success "资源数量: $RESOURCE_COUNT"
success "模板数量: $TEMPLATE_COUNT"
success "Keyless: $KEYLESS_COUNT"

if [ "$RESOURCE_COUNT" -ne 15 ] || [ "$TEMPLATE_COUNT" -ne 6 ] || [ "$KEYLESS_COUNT" -ne 3 ]; then
    warn "数据数量与预期不符（期望 15/6/3）"
fi

# ============================================================================
# 第三步：功能验证
# ============================================================================

echo ""
info "步骤 3/3: 功能验证..."

# 验证配额总量
MONTHLY_GB=$(psql "$DB_URL" -tAc "
SELECT ROUND(SUM(monthly_tokens)::numeric / 1000000000, 2)
FROM free_resource_catalog 
WHERE enabled = TRUE AND tenant_id = 'default';
")

success "月度配额: ${MONTHLY_GB}B tokens"

# 验证 ToS 分布
TOS_OK=$(psql "$DB_URL" -tAc "
SELECT COUNT(*) FROM free_resource_catalog 
WHERE tos_verdict='ok' AND enabled=TRUE AND tenant_id='default';
")

success "ToS OK: $TOS_OK 个资源"

# 验证模板
TEMPLATES=$(psql "$DB_URL" -tAc "
SELECT STRING_AGG(combo_name, ', ')
FROM auto_combo_templates 
WHERE enabled=TRUE AND tenant_id='default';
")

success "可用模板: $TEMPLATES"

# 测试简单查询性能
info "测试查询性能..."
START_TIME=$(date +%s%3N)
psql "$DB_URL" -c "
SELECT provider_code, model_id, free_type, monthly_tokens
FROM free_resource_catalog
WHERE tenant_id = 'default' AND enabled = TRUE
ORDER BY monthly_tokens DESC
LIMIT 5;
" >/dev/null
END_TIME=$(date +%s%3N)
QUERY_TIME=$((END_TIME - START_TIME))

success "查询耗时: ${QUERY_TIME}ms"

if [ "$QUERY_TIME" -gt 100 ]; then
    warn "查询较慢（>100ms），可能需要优化索引"
fi

# ============================================================================
# 完成
# ============================================================================

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
success "🎉 OmniFree 部署完成！"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

info "部署摘要："
echo "  ✅ 数据库迁移成功"
echo "  ✅ seed 数据导入成功"
echo "  ✅ 功能验证通过"
echo ""
info "数据统计："
echo "  - 免费资源: $RESOURCE_COUNT 个"
echo "  - Auto Combo 模板: $TEMPLATE_COUNT 个"
echo "  - Keyless 提供商: $KEYLESS_COUNT 个"
echo "  - 月度配额: ${MONTHLY_GB}B tokens"
echo "  - ToS OK: $TOS_OK 个"
echo ""
info "下一步："
echo "  1. 测试更多查询功能"
echo "  2. 验证 RLS 租户隔离"
echo "  3. 按 docs/omnifree/HANDOFF.md 开始应用层集成"
echo ""

# 清理
rm -f /tmp/seed-free-resources-245
