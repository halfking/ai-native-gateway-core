#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# OmniFree 一键部署脚本
# 用途: 自动执行数据库迁移 + 种子数据导入 + 健康检查

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

success() {
    echo -e "${GREEN}✅ $1${NC}"
}

warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

error() {
    echo -e "${RED}❌ $1${NC}"
    exit 1
}

# 检查环境变量
if [ -z "$DB_URL" ]; then
    error "请设置环境变量 DB_URL (例: postgres://user:pass@localhost/dbname)"
fi

# 检查依赖
info "检查依赖..."
command -v psql >/dev/null 2>&1 || error "需要安装 psql"
command -v go >/dev/null 2>&1 || error "需要安装 Go (1.21+)"

# 检查工作目录
if [ ! -f "go.mod" ]; then
    error "请在项目根目录执行此脚本"
fi

success "依赖检查通过"

# 1. 数据库迁移
info "执行数据库迁移..."
MIGRATION_FILE="sql/migrations/075-omnifree-schema.sql"

if [ ! -f "$MIGRATION_FILE" ]; then
    warn "迁移文件不存在: $MIGRATION_FILE"
    info "将在 Phase 1 实施时创建"
else
    psql "$DB_URL" -v ON_ERROR_STOP=1 -f "$MIGRATION_FILE" || error "数据库迁移失败"
    success "数据库迁移完成"
fi

# 2. 构建种子数据导入工具
info "构建种子数据导入工具..."
if [ ! -d "cmd/seed-free-resources" ]; then
    warn "种子导入工具尚未实现，跳过..."
else
    go build -o /tmp/seed-free-resources ./cmd/seed-free-resources || error "构建失败"
    success "构建完成"

    # 3. 导入种子数据
    info "导入种子数据..."
    /tmp/seed-free-resources \
        --db-url "$DB_URL" \
        --catalog configs/seed/free_resource_catalog.json \
        --templates configs/seed/auto_combo_templates.json \
        --keyless configs/seed/keyless_providers.json \
        || error "种子数据导入失败"
    success "种子数据导入完成"
fi

# 4. 验证数据
info "验证数据完整性..."
RESOURCES=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE enabled=TRUE;
" 2>/dev/null || echo "0")

TEMPLATES=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM auto_combo_templates WHERE enabled=TRUE;
" 2>/dev/null || echo "0")

KEYLESS=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM keyless_providers WHERE enabled=TRUE;
" 2>/dev/null || echo "0")

if [ "$RESOURCES" -gt 0 ]; then
    success "已启用免费资源: $RESOURCES"
else
    warn "未找到已启用的免费资源"
fi

if [ "$TEMPLATES" -gt 0 ]; then
    success "已启用 Auto Combo 模板: $TEMPLATES"
else
    warn "未找到已启用的 Auto Combo 模板"
fi

if [ "$KEYLESS" -gt 0 ]; then
    success "已启用 Keyless 提供商: $KEYLESS"
else
    warn "未找到已启用的 Keyless 提供商"
fi

# 5. 计算总配额
info "计算总免费配额..."
TOTAL_MONTHLY=$(psql "$DB_URL" -tAc "
    SELECT COALESCE(SUM(monthly_tokens), 0)
    FROM free_resource_catalog
    WHERE enabled=TRUE AND free_type IN ('recurring-monthly', 'keyless');
" 2>/dev/null || echo "0")

TOTAL_DAILY=$(psql "$DB_URL" -tAc "
    SELECT COALESCE(SUM(daily_tokens), 0)
    FROM free_resource_catalog
    WHERE enabled=TRUE AND free_type='recurring-daily';
" 2>/dev/null || echo "0")

if [ "$TOTAL_MONTHLY" -gt 0 ]; then
    MONTHLY_GB=$(echo "scale=2; $TOTAL_MONTHLY / 1000000000" | bc)
    success "月度免费配额: ${MONTHLY_GB}B tokens"
fi

if [ "$TOTAL_DAILY" -gt 0 ]; then
    DAILY_MB=$(echo "scale=2; $TOTAL_DAILY / 1000000" | bc)
    success "日度免费配额: ${DAILY_MB}M tokens"
fi

# 6. ToS 合规检查
info "ToS 合规统计..."
TOS_OK=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE tos_verdict='ok' AND enabled=TRUE;
" 2>/dev/null || echo "0")

TOS_AVOID=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE tos_verdict='avoid' AND enabled=TRUE;
" 2>/dev/null || echo "0")

success "ToS OK: $TOS_OK"
if [ "$TOS_AVOID" -gt 0 ]; then
    warn "ToS Avoid: $TOS_AVOID (建议禁用)"
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
success "🎉 OmniFree 部署完成!"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
info "下一步:"
echo "  1. 查看文档: docs/omnifree/00-OVERVIEW.md"
echo "  2. 运行健康检查: ./scripts/omnifree/healthcheck.sh"
echo "  3. 测试 API:"
echo "     curl -X POST http://localhost:8781/v1/chat/completions \\"
echo "       -H 'Authorization: Bearer \$TOKEN' \\"
echo "       -d '{\"model\":\"auto/free\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'"
echo ""
