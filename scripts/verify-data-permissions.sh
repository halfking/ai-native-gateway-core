#!/bin/bash
# 数据权限实施验证脚本
# 检查所有 admin 端点的实际权限保护状态

# ★ 专用构建缓存隔离（gocache 脚本族门判据③：在作用域内的脚本必须指向专用缓存；
#   2026-11-27 代并行车道收口——本文件的 echo 文本含 "go test" 字样 ⇒ 门的编译型
#   口径把它算进作用域 ⇒ 接上共享入口后判据自然绿；实际不改变脚本任何行为）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi

cd "$(dirname "$0")/.."

echo "=== 数据权限实施验证报告 ==="
echo "生成时间: $(date)"
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "## 1. 租户过滤检查"
echo ""

echo "### ✅ 已实现租户过滤的端点："
echo ""

# 检查使用 EffectiveTenantIDAll 的文件
echo "#### 使用 EffectiveTenantIDAll（super_admin看全部，tenant_admin看自己）："
grep -l "EffectiveTenantIDAll" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "EffectiveTenantIDAll" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "#### 使用 EffectiveTenantID（tenant_admin强制过滤）:"
grep -l "EffectiveTenantID\(" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "EffectiveTenantID\(" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "#### 使用 effectiveScopeTenant（会话相关）:"
grep -l "effectiveScopeTenant" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "effectiveScopeTenant" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "#### 使用 IsTenantAdmin 检查:"
grep -l "if IsTenantAdmin" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "if IsTenantAdmin" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "---"
echo ""

echo "## 2. 写操作保护检查"
echo ""

echo "### ✅ 已使用 RequireSuperAdminForWrite 的文件："
grep -l "RequireSuperAdminForWrite" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "RequireSuperAdminForWrite" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "### ⚠️ 包含 DELETE/UPDATE 但未检查的文件（需人工复核）："
for file in admin/*.go; do
    if [[ "$file" == *"_test.go"* ]]; then
        continue
    fi
    
    has_delete=$(grep -c "DELETE FROM" "$file" 2>/dev/null || echo 0)
    has_update=$(grep -c "UPDATE " "$file" 2>/dev/null || echo 0)
    has_check=$(grep -c "RequireSuperAdminForWrite\|assertKeyTenantScope\|CanAccessUserData\|RequireTenantMatch" "$file" 2>/dev/null || echo 0)
    
    if [ $has_delete -gt 0 ] || [ $has_update -gt 0 ]; then
        if [ $has_check -eq 0 ]; then
            echo -e "  ${RED}❌ $(basename $file): DELETE=$has_delete, UPDATE=$has_update, 无权限检查${NC}"
        else
            echo -e "  ${GREEN}✓ $(basename $file): DELETE=$has_delete, UPDATE=$has_update, 已有检查$has_check处${NC}"
        fi
    fi
done

echo ""
echo "---"
echo ""

echo "## 3. 用户级端点检查"
echo ""

echo "### 当前使用 user_id 过滤的文件："
grep -l "user_id.*WHERE\|WHERE.*user_id" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "user_id" "$file")
    echo "  - $(basename $file): $count 次提及"
done

echo ""
echo "### 使用 ownerScopeClause（用户级过滤）："
grep -l "ownerScopeClause" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "ownerScopeClause" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "### 使用 IsRegularUser 检查："
grep -l "IsRegularUser" admin/*.go | grep -v "_test.go" | while read file; do
    count=$(grep -c "IsRegularUser" "$file")
    echo "  - $(basename $file): $count 处"
done

echo ""
echo "---"
echo ""

echo "## 4. 新增权限 API 使用情况"
echo ""

for func in "EffectiveUserID" "CanAccessTenant" "CanAccessUserData" "RequireUserOrHigher" "RequireTenantMatch" "RequireUserMatch"; do
    count=$(grep -r "$func" admin/*.go 2>/dev/null | grep -v "_test.go" | grep -v "context_enhanced.go" | grep -v "permission_fix_examples.go" | wc -l | tr -d ' ')
    if [ "$count" -gt 0 ]; then
        echo -e "  ${GREEN}✓ $func: $count 处使用${NC}"
    else
        echo -e "  ${YELLOW}⚠ $func: 尚未使用${NC}"
    fi
done

echo ""
echo "---"
echo ""

echo "## 5. 高风险端点快速检查"
echo ""

# 检查特定高风险文件
high_risk_files=(
    "admin/users.go"
    "admin/keys.go"
    "admin/provider_credential.go"
    "admin/session_management_api.go"
    "admin/session_trash.go"
    "admin/logs.go"
    "admin/usage.go"
)

for file in "${high_risk_files[@]}"; do
    if [ -f "$file" ]; then
        has_tenant_filter=$(grep -c "EffectiveTenantID\|effectiveScopeTenant\|IsTenantAdmin" "$file" 2>/dev/null || echo 0)
        has_write_check=$(grep -c "RequireSuperAdminForWrite\|assertKeyTenantScope" "$file" 2>/dev/null || echo 0)
        
        if [ $has_tenant_filter -gt 0 ] && [ $has_write_check -gt 0 ]; then
            echo -e "  ${GREEN}✓ $(basename $file): 租户过滤=$has_tenant_filter, 写保护=$has_write_check${NC}"
        elif [ $has_tenant_filter -gt 0 ]; then
            echo -e "  ${YELLOW}⚠ $(basename $file): 仅租户过滤=$has_tenant_filter, 写保护=$has_write_check${NC}"
        elif [ $has_write_check -gt 0 ]; then
            echo -e "  ${YELLOW}⚠ $(basename $file): 仅写保护=$has_write_check, 租户过滤=$has_tenant_filter${NC}"
        else
            echo -e "  ${RED}❌ $(basename $file): 未检测到权限保护${NC}"
        fi
    fi
done

echo ""
echo "---"
echo ""

echo "## 6. 测试覆盖"
echo ""

echo "### 权限相关测试文件："
find admin -name "*_test.go" -type f | while read file; do
    if grep -q "AuthContext\|IsTenantAdmin\|IsSuperAdmin\|EffectiveTenant" "$file" 2>/dev/null; then
        test_count=$(grep -c "func Test" "$file")
        echo "  - $(basename $file): $test_count 个测试"
    fi
done

echo ""
echo "=== 验证完成 ==="
echo ""
echo "建议："
echo "1. 重点关注标记为 ❌ 的文件"
echo "2. 对标记为 ⚠️ 的文件进行人工审查"
echo "3. 运行测试套件验证权限逻辑"
echo ""
echo "运行测试："
echo "  go test -v ./admin -run TestContextEnhanced"
echo "  go test -v ./admin -run TestRoleRules"
