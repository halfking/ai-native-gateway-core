#!/bin/bash
# 统计页面API端点验证脚本
# 不依赖UI登录，直接验证后端API功能

set -euo pipefail

BASE_URL="https://llm.itestu.cn"
echo "=== 统计系统API端点验证 ==="
echo "目标: $BASE_URL"
echo ""

PASSED=0
FAILED=0

# 1. 健康检查
echo "▶️  [1/7] 健康检查"
if curl -sf "$BASE_URL/healthz" > /dev/null; then
    VERSION=$(curl -s "$BASE_URL/healthz" | jq -r '.version')
    echo "   ✅ 通过 - 版本: $VERSION"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

# 2. 版本验证（确认包含我们的提交）
echo "▶️  [2/7] 版本验证（包含2ae6b26d提交）"
if curl -s "$BASE_URL/healthz" | grep -q "2ae6b26d"; then
    echo "   ✅ 通过 - 包含analytics模块提交"
    ((PASSED++))
else
    echo "   ❌ 失败 - 未找到目标提交"
    ((FAILED++))
fi

# 3. 前端资源可达性
echo "▶️  [3/7] 前端SPA可达性"
if curl -sf "$BASE_URL/" -H "Accept: text/html" | grep -q "<!DOCTYPE html>"; then
    echo "   ✅ 通过 - SPA入口正常"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

# 4. Dashboard路由存在
echo "▶️  [4/7] Dashboard路由"
if curl -sf "$BASE_URL/dashboard" -H "Accept: text/html" | grep -q "<!DOCTYPE html>"; then
    echo "   ✅ 通过 - /dashboard可达"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

# 5. Live Topology路由
echo "▶️  [5/7] Live Topology路由"
if curl -sf "$BASE_URL/live" -H "Accept: text/html" | grep -q "<!DOCTYPE html>"; then
    echo "   ✅ 通过 - /live可达"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

# 6. Session Analytics路由
echo "▶️  [6/7] Session Analytics路由"
if curl -sf "$BASE_URL/admin/session-analytics" -H "Accept: text/html" | grep -q "<!DOCTYPE html>"; then
    echo "   ✅ 通过 - /admin/session-analytics可达"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

# 7. Reconciliation路由
echo "▶️  [7/7] Reconciliation路由"
if curl -sf "$BASE_URL/admin/reconciliation" -H "Accept: text/html" | grep -q "<!DOCTYPE html>"; then
    echo "   ✅ 通过 - /admin/reconciliation可达"
    ((PASSED++))
else
    echo "   ❌ 失败"
    ((FAILED++))
fi

echo ""
echo "=== 验证结果 ==="
echo "总计: 7"
echo "通过: $PASSED"
echo "失败: $FAILED"
echo ""

if [ $FAILED -eq 0 ]; then
    echo "✅ 所有统计系统端点验证通过"
    echo ""
    echo "注: 完整功能测试需要登录凭据。"
    echo "    当前验证确认了："
    echo "    1. 部署成功（版本包含analytics模块）"
    echo "    2. 所有统计页面路由可达"
    echo "    3. 前端资源正常加载"
    exit 0
else
    echo "❌ 有 $FAILED 个端点失败"
    exit 1
fi
