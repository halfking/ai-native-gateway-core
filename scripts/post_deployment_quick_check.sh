#!/bin/bash
# post_deployment_quick_check.sh — 部署后快速检查 (2026-11-20)

echo "=== 部署后快速检查 ==="
echo ""

BASE_URL="https://llm.itestu.cn"

echo "1. 检查服务版本:"
version_info=$(curl -s "$BASE_URL/version" 2>/dev/null)
if [ -n "$version_info" ]; then
    echo "$version_info" | jq -r '"  版本: \(.version)\n  Git SHA: \(.git_sha[0:8])\n  构建时间: \(.built_at)"' 2>/dev/null || echo "  $version_info"
else
    echo "  ⚠️  服务未响应"
    exit 1
fi

echo ""
echo "2. 检查运营看板端点(无需认证):"
board_status=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/admin/dashboard/board?days=7" 2>/dev/null)
echo "  /api/admin/dashboard/board: HTTP $board_status"

echo ""
echo "3. 检查Analytics端点(无需认证):"
analytics_status=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/admin/analytics/model/gpt-4?days=7" 2>/dev/null)
echo "  /api/admin/analytics/model/gpt-4: HTTP $analytics_status"

echo ""
if [ "$board_status" = "401" ] || [ "$board_status" = "200" ]; then
    if [ "$analytics_status" = "401" ] || [ "$analytics_status" = "200" ]; then
        echo "✅ 基础检查通过!服务正常运行。"
        echo ""
        echo "📝 下一步:"
        echo "  1. 使用认证令牌进行完整测试:"
        echo "     AUTH_TOKEN='Bearer {token}' bash scripts/analytics_production_verification.sh"
        echo ""
        echo "  2. 浏览器访问:"
        echo "     https://llm.itestu.cn/admin/board-operational"
        exit 0
    fi
fi

echo "⚠️  检查未完全通过,请查看上述状态码。"
echo ""
echo "预期状态码:"
echo "  - 200: 成功(有有效令牌)"
echo "  - 401: 未授权(需要令牌,但端点存在)"
echo "  - 404: 端点不存在(可能部署未完成)"
echo "  - 502/503: 服务不可用"

exit 1
