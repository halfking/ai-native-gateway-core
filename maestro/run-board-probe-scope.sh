#!/usr/bin/env bash
# maestro/run-board-probe-scope.sh —— 看板探测口径切换 flow 的实跑入口。
#
# 与 run-stage-tree.sh 同款理由：web 驱动默认视口 1024×625 看不到下方筛选条
# 的切换器 ⇒ 必须 --screen-size 1440x2000（板面较长，含英雄区到筛选条全距）。
#
# 用法：
#   BASE_URL=https://llm.itestu.cn ADMIN_USER=admin ADMIN_PASSWORD='***' \
#     bash maestro/run-board-probe-scope.sh [输出目录]
#
# ★ 口令只从环境变量读，不进仓内文件。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-/tmp/maestro-out-board-probe}"

: "${BASE_URL:?需要 BASE_URL，例如 https://llm.itestu.cn}"
: "${ADMIN_USER:?需要 ADMIN_USER}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（只从环境变量读，不进仓内文件）}"

export PATH="$HOME/.maestro/bin:$PATH"
if [ -z "${JAVA_HOME:-}" ] && [ -d "$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home" ]; then
  export JAVA_HOME="$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home"
fi

echo "▶ 输出目录：$OUT_DIR"
echo "▶ 视口：1440x2000"

maestro test \
  --headless \
  --no-ansi \
  --screen-size 1440x2000 \
  --test-output-dir "$OUT_DIR" \
  -e BASE_URL="$BASE_URL" \
  -e ADMIN_USER="$ADMIN_USER" \
  -e "ADMIN_PASSWORD=$ADMIN_PASSWORD" \
  -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME:-系统管理员}" \
  "$ROOT/maestro/flows/15-board-probe-scope.yaml"
