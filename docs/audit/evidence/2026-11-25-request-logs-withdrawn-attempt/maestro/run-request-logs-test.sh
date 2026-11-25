#!/usr/bin/env bash
# maestro/run-request-logs-test.sh —— request-logs 页面超时优化验证测试
#
# 用法：
#   # 测试本地开发环境
#   ./maestro/run-request-logs-test.sh
#
#   # 测试 211 生产环境
#   BASE_URL=http://192.168.31.211:8783 ./maestro/run-request-logs-test.sh
#
# 前置条件：
#   - 仓根存在 .env.local 文件，包含 ADMIN_USERNAME 和 ADMIN_PASSWORD
#   - 或环境变量 ADMIN_USERNAME 和 ADMIN_PASSWORD 已设置

set -euo pipefail

cd "$(dirname "$0")/.."

FLOW="maestro/flows/14-request-logs-timeout-optimization.yaml"
BASE_URL="${BASE_URL:-http://127.0.0.1:8782}"
SCREEN_SIZE="${SCREEN_SIZE:-1440x1400}"

echo "════════════════════════════════════════════════════════════════"
echo "request-logs 页面超时优化验证测试"
echo "════════════════════════════════════════════════════════════════"
echo "目标服务: ${BASE_URL}"
echo "测试流程: ${FLOW}"
echo "屏幕尺寸: ${SCREEN_SIZE}"
echo ""

# 检查 .env.local 是否存在
if [ -f ".env.local" ]; then
    echo "✓ 找到 .env.local，读取凭据..."
    # 使用 run-local.sh 运行（它会处理凭据）
    exec bash maestro/run-local.sh "${FLOW}"
else
    echo "✗ 未找到 .env.local 文件"
    echo ""
    echo "需要在仓根创建 .env.local 文件，内容示例："
    echo ""
    echo "  ADMIN_USERNAME=admin"
    echo "  ADMIN_PASSWORD=your_password_here"
    echo ""
    echo "或者设置环境变量："
    echo ""
    echo "  export ADMIN_USERNAME=admin"
    echo "  export ADMIN_PASSWORD=your_password_here"
    echo "  BASE_URL=${BASE_URL} ./maestro/run-request-logs-test.sh"
    echo ""
    exit 1
fi
