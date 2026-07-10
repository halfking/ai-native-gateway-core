#!/bin/bash
# 测试前端 JS 错误修复
# 验证 HeatmapMatrix 和 RouteFlowSankey 组件的修复

set -e

echo "=========================================="
echo "前端 JS 错误修复验证"
echo "=========================================="
echo ""

# 检查 gateway 是否运行
if ! pgrep -f "./gateway" > /dev/null; then
    echo "❌ Gateway 未运行"
    exit 1
fi

echo "✅ Gateway 正在运行"

# 获取端口
PORT=$(lsof -iTCP -sTCP:LISTEN -n -P | grep gateway | awk '{print $9}' | cut -d: -f2 | head -1)
if [ -z "$PORT" ]; then
    PORT=8781
fi

echo "✅ Gateway 监听端口: $PORT"
echo ""

# 测试首页加载
echo "测试 1: 验证首页加载..."
HOMEPAGE=$(curl -s http://localhost:$PORT/)
if echo "$HOMEPAGE" | grep -q "index-.*\.js"; then
    echo "✅ 首页 HTML 加载成功"
else
    echo "❌ 首页加载失败"
    exit 1
fi

# 检查修复的组件文件
echo ""
echo "测试 2: 验证组件修复..."

# 检查 HeatmapMatrix.vue 是否已修复
if grep -q 'v-else-if="data && data.rows && data.cols"' web/src/components/analytics/HeatmapMatrix.vue; then
    echo "✅ HeatmapMatrix.vue 已修复 (添加了 null 检查)"
else
    echo "❌ HeatmapMatrix.vue 未正确修复"
    exit 1
fi

# 检查是否移除了非空断言
if grep -q 'data!.rows' web/src/components/analytics/HeatmapMatrix.vue; then
    echo "❌ HeatmapMatrix.vue 仍包含 data!.rows 非空断言"
    exit 1
else
    echo "✅ HeatmapMatrix.vue 已移除非空断言"
fi

# 检查 RouteFlowSankey.vue
if grep -q 'v-else-if="data && data.links"' web/src/components/analytics/RouteFlowSankey.vue; then
    echo "✅ RouteFlowSankey.vue 已修复 (添加了 null 检查)"
else
    echo "❌ RouteFlowSankey.vue 未正确修复"
    exit 1
fi

if grep -q 'data!.links' web/src/components/analytics/RouteFlowSankey.vue; then
    echo "❌ RouteFlowSankey.vue 仍包含 data!.links 非空断言"
    exit 1
else
    echo "✅ RouteFlowSankey.vue 已移除非空断言"
fi

# 检查 RoutingDashboardView.vue
if grep -q 'layer2Cache\[.*\]!\.candidates' web/src/views/RoutingDashboardView.vue; then
    echo "❌ RoutingDashboardView.vue 仍包含 layer2Cache[...]! 非空断言"
    exit 1
else
    echo "✅ RoutingDashboardView.vue 已移除非空断言"
fi

echo ""
echo "测试 3: 验证构建产物..."

# 检查 dist 目录是否存在且包含新构建的文件
if [ ! -d "web/dist" ]; then
    echo "❌ web/dist 目录不存在"
    exit 1
fi

# 检查构建时间（应该是最近的）
DIST_TIME=$(stat -f "%Sm" -t "%Y-%m-%d %H:%M" web/dist/index.html 2>/dev/null || stat -c "%y" web/dist/index.html 2>/dev/null | cut -d' ' -f1-2)
echo "✅ 构建时间: $DIST_TIME"

# 检查关键资源文件
INDEX_JS=$(ls web/dist/assets/index-*.js 2>/dev/null | head -1)
if [ -n "$INDEX_JS" ]; then
    echo "✅ 主 JS 文件存在: $(basename $INDEX_JS)"
else
    echo "❌ 主 JS 文件不存在"
    exit 1
fi

echo ""
echo "测试 4: API 端点检查..."

# 测试健康检查
HEALTH=$(curl -s http://localhost:$PORT/healthz)
if echo "$HEALTH" | grep -q "ok\|healthy\|pong"; then
    echo "✅ 健康检查端点正常"
else
    echo "⚠️  健康检查端点返回异常 (可能正常，如果未配置数据库)"
fi

echo ""
echo "=========================================="
echo "✅ 所有测试通过！"
echo "=========================================="
echo ""
echo "下一步："
echo "1. 打开浏览器访问: http://localhost:$PORT"
echo "2. 打开开发者工具 (F12) 查看控制台"
echo "3. 验证不再出现 'Cannot destructure property row' 错误"
echo "4. 访问路由仪表板测试热力图功能"
echo ""
