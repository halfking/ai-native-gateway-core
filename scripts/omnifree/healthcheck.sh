#!/bin/bash
# OmniFree 健康检查脚本
# 用途: 验证免费资源系统各组件运行状态

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info() { echo -e "${BLUE}🔍 $1${NC}"; }
success() { echo -e "${GREEN}✅ $1${NC}"; }
warn() { echo -e "${YELLOW}⚠️  $1${NC}"; }
error() { echo -e "${RED}❌ $1${NC}"; }

ERRORS=0

# 检查环境变量
if [ -z "$DB_URL" ]; then
    error "请设置环境变量 DB_URL"
    exit 1
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
info "OmniFree 健康检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 1. 数据库连接
info "检查数据库连接..."
if psql "$DB_URL" -c "SELECT 1" >/dev/null 2>&1; then
    success "数据库连接正常"
else
    error "数据库连接失败"
    ((ERRORS++))
fi

# 2. 表结构
info "检查表结构..."
TABLES=("free_resource_catalog" "free_quota_tracker" "auto_combo_templates" "keyless_providers")
for table in "${TABLES[@]}"; do
    if psql "$DB_URL" -c "\d $table" >/dev/null 2>&1; then
        success "表 $table 存在"
    else
        error "表 $table 不存在"
        ((ERRORS++))
    fi
done

# 3. 免费资源统计
info "免费资源统计..."
RESOURCES=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM free_resource_catalog WHERE enabled=TRUE;" 2>/dev/null || echo "0")
RESOURCES_TOTAL=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM free_resource_catalog;" 2>/dev/null || echo "0")

if [ "$RESOURCES" -gt 0 ]; then
    success "已启用: $RESOURCES / 总计: $RESOURCES_TOTAL"
else
    warn "未找到已启用的免费资源"
    ((ERRORS++))
fi

# 4. 配额追踪健康
info "配额追踪健康检查..."
TODAY_WINDOWS=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_quota_tracker
    WHERE window_start >= current_date;
" 2>/dev/null || echo "0")

EXHAUSTED=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_quota_tracker
    WHERE window_start >= current_date AND is_exhausted=TRUE;
" 2>/dev/null || echo "0")

success "当日追踪窗口: $TODAY_WINDOWS"
if [ "$EXHAUSTED" -gt 0 ]; then
    warn "已耗尽窗口: $EXHAUSTED"
else
    success "无耗尽窗口"
fi

# 5. Auto Combo 模板
info "Auto Combo 模板检查..."
TEMPLATES=$(psql "$DB_URL" -tAc "SELECT COUNT(*) FROM auto_combo_templates WHERE enabled=TRUE;" 2>/dev/null || echo "0")

if [ "$TEMPLATES" -ge 6 ]; then
    success "已启用模板: $TEMPLATES (预期 ≥6)"
else
    warn "已启用模板: $TEMPLATES (预期 ≥6)"
fi

# 6. Keyless 提供商
info "Keyless 提供商检查..."
KEYLESS=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM keyless_providers WHERE enabled=TRUE;
" 2>/dev/null || echo "0")

KEYLESS_ALLOWLIST=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM keyless_providers
    WHERE enabled=TRUE AND allowlist_in_auto_combo=TRUE;
" 2>/dev/null || echo "0")

success "已启用: $KEYLESS (白名单: $KEYLESS_ALLOWLIST)"

# 7. 配额池去重
info "配额池去重验证..."
POOL_COUNT=$(psql "$DB_URL" -tAc "
    SELECT COUNT(DISTINCT COALESCE(pool_key, provider_code || ':' || model_id))
    FROM free_resource_catalog
    WHERE enabled=TRUE AND free_type IN ('recurring-monthly', 'recurring-daily');
" 2>/dev/null || echo "0")

success "独立配额池: $POOL_COUNT"

# 8. ToS 合规
info "ToS 合规检查..."
TOS_OK=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE tos_verdict='ok' AND enabled=TRUE;
" 2>/dev/null || echo "0")

TOS_AVOID=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE tos_verdict='avoid' AND enabled=TRUE;
" 2>/dev/null || echo "0")

TOS_UNKNOWN=$(psql "$DB_URL" -tAc "
    SELECT COUNT(*) FROM free_resource_catalog WHERE tos_verdict='unknown' AND enabled=TRUE;
" 2>/dev/null || echo "0")

success "OK: $TOS_OK | Avoid: $TOS_AVOID | Unknown: $TOS_UNKNOWN"
if [ "$TOS_AVOID" -gt 0 ]; then
    warn "建议禁用 $TOS_AVOID 个 ToS Avoid 资源"
fi

# 9. API 端点测试 (可选)
if [ -n "$GATEWAY_URL" ] && [ -n "$TOKEN" ]; then
    info "测试 auto/free 端点..."
    
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST "$GATEWAY_URL/v1/chat/completions" \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"model":"auto/free","messages":[{"role":"user","content":"test"}],"max_tokens":5}' \
        2>/dev/null || echo "000")
    
    if [ "$STATUS" = "200" ]; then
        success "auto/free 路由正常 (HTTP 200)"
    elif [ "$STATUS" = "000" ]; then
        warn "auto/free 端点无法访问 (请检查 GATEWAY_URL)"
    else
        error "auto/free 路由失败 (HTTP $STATUS)"
        ((ERRORS++))
    fi
else
    warn "跳过 API 测试 (需设置 GATEWAY_URL 和 TOKEN)"
fi

# 10. 总结
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if [ $ERRORS -eq 0 ]; then
    success "🎉 健康检查通过! (0 错误)"
    exit 0
else
    error "健康检查失败! ($ERRORS 错误)"
    exit 1
fi
