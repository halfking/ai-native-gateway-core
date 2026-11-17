#!/usr/bin/env bash
# maestro/run-stage-tree.sh —— 请求阶段树（/live2）的 Maestro 实跑入口。
#
# ★ 为什么必须用这个脚本、而不是直接 `maestro test`：
#   Maestro 2.11.0 的 **web 驱动**视口固定 **1024×625**，view hierarchy 只覆盖
#   视口 ⇒ 折叠线以下的内容 `assertVisible` 一律判 false；
#   而本应用壳的滚动容器是 **`.main-body`**（不是 window）⇒ Maestro 的
#   `scrollVertical()` / `scrollUntilVisible` 滚不动（README §6.6 ⑥ 实测）。
#   ⇒ 只能**把视口放大到桌面尺寸**：`--screen-size 1440x1400`。
#
# 用法：
#   BASE_URL=https://llm.kxpms.cn ADMIN_USER=admin ADMIN_PASSWORD='***' \
#     bash maestro/run-stage-tree.sh [输出目录]
#
# ★ 口令只从环境变量读，**不进仓内文件**；输出目录默认 /tmp/maestro-out-stage-tree。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-/tmp/maestro-out-stage-tree}"

: "${BASE_URL:?需要 BASE_URL，例如 https://llm.kxpms.cn}"
: "${ADMIN_USER:?需要 ADMIN_USER}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（只从环境变量读，不进仓内文件）}"

export PATH="$HOME/.maestro/bin:$PATH"
if [ -z "${JAVA_HOME:-}" ] && [ -d "$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home" ]; then
  export JAVA_HOME="$HOME/.local/jdk/jdk-21.0.12.1+1/Contents/Home"
fi

echo "▶ 输出目录：$OUT_DIR"
echo "▶ 视口：1440x1400（web 驱动默认 1024×625 看不到折叠线以下，见脚本头注释）"

maestro test \
  --headless \
  --no-ansi \
  --screen-size 1440x1400 \
  --test-output-dir "$OUT_DIR" \
  -e BASE_URL="$BASE_URL" \
  -e ADMIN_USER="$ADMIN_USER" \
  -e "ADMIN_PASSWORD=$ADMIN_PASSWORD" \
  "$ROOT/maestro/flows/12-live-stage-tree.yaml"
