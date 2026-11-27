#!/usr/bin/env bash
# maestro/run-reconciliation-business-toggle.sh —— 对账页「仅业务流量」按钮状态切换测试。
#
# 用法：
#   BASE_URL=https://llm.itestu.cn ADMIN_USER=admin ADMIN_PASSWORD='...' \
#     bash maestro/run-reconciliation-business-toggle.sh [输出目录]
#
# ★ 口令只从环境变量读，不进仓内文件。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-/tmp/maestro-out-reconciliation-toggle}"

: "${BASE_URL:?需要 BASE_URL，例如 https://llm.itestu.cn}"
: "${ADMIN_USER:?需要 ADMIN_USER}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（只从环境变量读，不进仓内文件）}"

export PATH="$HOME/.maestro/bin:$PATH"
if [ -z "${JAVA_HOME:-}" ] && [ -d "$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home" ]; then
  export JAVA_HOME="$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home"
fi

echo "▶ 输出目录：$OUT_DIR"
echo "▶ 测试 flow：22-reconciliation-business-traffic-toggle.yaml"

maestro test \
  --headless \
  --screen-size 1440x900 \
  --format junit \
  --output "$OUT_DIR" \
  --env BASE_URL="$BASE_URL" \
  --env ADMIN_USER="$ADMIN_USER" \
  --env ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  --env ADMIN_DISPLAY_NAME="超级管理员" \
  "$ROOT/maestro/flows/22-reconciliation-business-traffic-toggle.yaml"

echo "✓ 测试完成，报告：$OUT_DIR"
