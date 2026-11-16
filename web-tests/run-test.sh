#!/bin/bash

# 租户模型定价页面测试快速启动脚本

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "==================================="
echo "租户模型定价页面浏览器测试"
echo "==================================="
echo ""

# 检查 Node.js
if ! command -v node &> /dev/null; then
    echo "❌ 错误: 未找到 Node.js"
    echo "   请先安装 Node.js: https://nodejs.org/"
    exit 1
fi

echo "✓ Node.js 版本: $(node --version)"

# 检查 npm
if ! command -v npm &> /dev/null; then
    echo "❌ 错误: 未找到 npm"
    exit 1
fi

echo "✓ npm 版本: $(npm --version)"
echo ""

# 安装依赖
if [ ! -d "node_modules" ]; then
    echo "📦 安装依赖..."
    npm install
    echo ""
fi

# 安装 Playwright 浏览器（如果需要）
if [ ! -d "$HOME/.cache/ms-playwright" ] && [ ! -d "$HOME/Library/Caches/ms-playwright" ]; then
    echo "🌐 安装 Playwright 浏览器..."
    npx playwright install chromium
    echo ""
fi

# 环境变量配置
export BASE_URL="${BASE_URL:-http://localhost:8090}"
export TEST_USERNAME="${TEST_USERNAME:-admin}"
export TEST_PASSWORD="${TEST_PASSWORD:-admin123}"

echo "📋 测试配置:"
echo "   BASE_URL: $BASE_URL"
echo "   USERNAME: $TEST_USERNAME"
echo ""

# 检查服务是否可访问
echo "🔍 检查服务连通性..."
if command -v curl &> /dev/null; then
    if curl -s -o /dev/null -w "%{http_code}" "$BASE_URL" | grep -q "^[23]"; then
        echo "✓ 服务可访问: $BASE_URL"
    else
        echo "⚠️  警告: 无法访问 $BASE_URL"
        echo "   请确保服务正在运行"
    fi
else
    echo "⚠️  未找到 curl，跳过连通性检查"
fi
echo ""

# 运行测试
echo "🧪 运行测试..."
echo "-----------------------------------"
echo ""

# 根据参数决定运行模式
case "${1:-}" in
    "headed")
        echo "▶️  以可视化模式运行测试"
        npx playwright test tenant-models-pricing.spec.ts --headed
        ;;
    "debug")
        echo "🐛 以调试模式运行测试"
        npx playwright test tenant-models-pricing.spec.ts --debug
        ;;
    "ui")
        echo "🎨 启动 Playwright UI 模式"
        npx playwright test tenant-models-pricing.spec.ts --ui
        ;;
    "report")
        echo "📊 生成测试报告"
        npx playwright test tenant-models-pricing.spec.ts --reporter=html
        npx playwright show-report
        ;;
    *)
        echo "▶️  以无头模式运行测试"
        npx playwright test tenant-models-pricing.spec.ts
        ;;
esac

TEST_EXIT_CODE=$?

echo ""
echo "-----------------------------------"

if [ $TEST_EXIT_CODE -eq 0 ]; then
    echo "✅ 测试通过!"
    echo ""
    echo "💡 提示:"
    echo "   - 查看报告: npm run test:report"
    echo "   - 可视化运行: ./run-test.sh headed"
    echo "   - 调试模式: ./run-test.sh debug"
else
    echo "❌ 测试失败 (退出码: $TEST_EXIT_CODE)"
    echo ""
    echo "💡 故障排查:"
    echo "   1. 检查服务是否运行: curl $BASE_URL"
    echo "   2. 验证登录凭据是否正确"
    echo "   3. 查看截图: ls -la test-results/"
    echo "   4. 以可视化模式重试: ./run-test.sh headed"
fi

echo ""
echo "==================================="

exit $TEST_EXIT_CODE
