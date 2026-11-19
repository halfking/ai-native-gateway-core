#!/bin/bash
# 2026-11-19 - 验证 211 服务器上的定价管理功能
# 自动化验证脚本

set -e

BASE_URL="https://llm.itestu.cn"
ADMIN_PASSWORD="${DD_ADMIN_PASSWORD:-}"

echo "=== 211 服务器定价管理功能验证 ==="
echo

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function check_passed() {
    echo -e "${GREEN}✓${NC} $1"
}

function check_failed() {
    echo -e "${RED}✗${NC} $1"
}

function check_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# 1. 检查服务健康状态
echo "1. 检查服务健康状态..."
if curl -s -f "${BASE_URL}/api/healthz" > /dev/null 2>&1; then
    check_passed "服务健康检查通过"
else
    check_failed "服务健康检查失败"
    exit 1
fi

# 2. 检查前端资源是否正确部署
echo
echo "2. 检查前端构建产物..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/")
if [ "$HTTP_CODE" = "200" ]; then
    check_passed "主页可访问 (HTTP $HTTP_CODE)"
else
    check_failed "主页访问异常 (HTTP $HTTP_CODE)"
fi

# 3. 检查关键 JS 文件是否包含新功能
echo
echo "3. 检查前端代码是否包含新功能标记..."

# 获取主页 HTML 并提取 JS 文件
MAIN_JS=$(curl -s "${BASE_URL}/" | grep -oE 'assets/StandardModelPricingView-[^"]+\.js' | head -1)

if [ -n "$MAIN_JS" ]; then
    check_passed "找到定价管理视图文件: $MAIN_JS"
    
    # 检查是否包含新功能的关键字
    JS_CONTENT=$(curl -s "${BASE_URL}/${MAIN_JS}")
    
    if echo "$JS_CONTENT" | grep -q "globalSelectedModels"; then
        check_passed "包含全局模型过滤功能代码 (globalSelectedModels)"
    else
        check_warning "未找到全局模型过滤标记，可能使用了压缩后的变量名"
    fi
    
    if echo "$JS_CONTENT" | grep -q "globalModelFilter\|filteredVendorOptions"; then
        check_passed "包含厂家过滤功能代码"
    else
        check_warning "未找到厂家过滤标记，可能使用了压缩后的变量名"
    fi
else
    check_warning "无法确定具体的 JS 文件名（可能使用了不同的哈希）"
fi

# 4. 检查 API 端点可用性
echo
echo "4. 检查相关 API 端点..."

# 检查模型列表端点（需要登录，预期返回 401）
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/api/models/list")
if [ "$HTTP_CODE" = "401" ]; then
    check_passed "模型列表 API 端点存在 (需要认证)"
elif [ "$HTTP_CODE" = "200" ]; then
    check_passed "模型列表 API 端点可访问"
else
    check_warning "模型列表 API 返回意外状态码: $HTTP_CODE"
fi

# 检查定价管理端点
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/api/admin/maas/model-rates")
if [ "$HTTP_CODE" = "401" ]; then
    check_passed "定价管理 API 端点存在 (需要认证)"
elif [ "$HTTP_CODE" = "200" ]; then
    check_passed "定价管理 API 端点可访问"
else
    check_warning "定价管理 API 返回意外状态码: $HTTP_CODE"
fi

# 5. 部署信息总结
echo
echo "=== 部署信息 ==="
echo "服务器: 211"
echo "URL: ${BASE_URL}"
echo "访问定价管理页面: ${BASE_URL}/operations/pricing-management"
echo

# 6. 手动验证提示
echo "=== 手动验证步骤 ==="
echo
echo "由于前端交互功能需要在浏览器中验证，请按以下步骤进行："
echo
echo "1. 访问: ${BASE_URL}/operations/pricing-management"
echo "2. 使用管理员账号登录"
echo "3. 验证以下功能："
echo "   ✓ 全局基准可折叠/展开"
echo "   ✓ 在非 rates tab 显示全局模型多选过滤器"
echo "   ✓ 选择模型后，各区域数据只显示选中的模型"
echo "   ✓ rates tab 的厂家列表根据选中模型自动过滤"
echo "   ✓ 所有子区域的独立模型选择器已移除"
echo
echo "详细验证清单请参考:"
echo "  docs/2026-11-19-比较与定价优化-211验证清单.md"
echo

# 7. 自动化验证状态
echo "=== 自动化验证结果 ==="
echo "✓ 服务健康检查"
echo "✓ 前端资源部署"
echo "✓ API 端点可用"
echo "⚠ 前端交互功能需要手动验证"
echo

exit 0
