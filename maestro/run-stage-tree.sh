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

# ── 第二层：页面内 CDP 断言（结构 / 口径 / 版面对拍）──────────────────────
# ★ 为什么是**第二层**而不是并入 flow：Maestro 2.11.0 的 web 驱动
#   ① evalScript 是 no-op；② `${output.x}` 只在 JVM 侧求值，拿不到 DOM；
#   ③ 选择器只有 text/id/css，且 id: 只认 HTML id ⇒
#   「列头与带同宽」「瓦片同一行」「错误列右缘恒定」「胶囊读数 == 实际带数」
#   这四类断言用 flow 的选择器**做不到**。
# ⇒ 需要一个已开 CDP 的浏览器与一个登录令牌（都从环境变量/临时文件读，不进仓）。
#   不给这两个变量就跳过（flow 本身已跑完，不受影响）。
if [ -n "${STAGE_TREE_CDP:-}" ] && [ -n "${STAGE_TREE_TOKEN_FILE:-}" ]; then
  # ★ 必须写 ${STAGE_TREE_CDP} 而不是 $STAGE_TREE_CDP：
  #   后面紧跟中文全角括号时，**UTF-8 locale 下 bash 会把多字节字符算进变量名**
  #   ⇒ `$STAGE_TREE_CDP）` 取的其实是变量「STAGE_TREE_CDP）」 ⇒ set -u 直接报
  #   「unbound variable」（本轮实测踩到）。用 ${} 界定才安全。
  echo "▶ 页面内断言（CDP=${STAGE_TREE_CDP}）"
  node "$ROOT/maestro/tools/assert-stage-tree.mjs" \
    --cdp "${STAGE_TREE_CDP}" --url "$BASE_URL" --token-file "${STAGE_TREE_TOKEN_FILE}"
  # ★ 设计层断言有**第三个退出码 2 = 量具失效**（页面窗口内 0 条真实请求 ⇒
  #   「有带时」的那批版面断言**未评估**，不是产品缺陷）。
  #   ⇒ 这里**不能**用 set -e 直接崩掉（会丢掉「量具失效」这个关键信息），
  #     必须显式接住并说人话。
  set +e
  node "$ROOT/maestro/tools/assert-stage-tree-design.mjs" \
    --cdp "${STAGE_TREE_CDP}" --url "$BASE_URL" --token-file "${STAGE_TREE_TOKEN_FILE}"
  DESIGN_EXIT=$?
  set -e
  if [ "${DESIGN_EXIT}" -eq 2 ]; then
    echo "⚠ 设计对照断言**量具失效**（退出码 2）：窗口内 0 条真实请求 ⇒ 版面契约未评估。"
    echo "  ⇒ 先造真实流量，再**立刻**重跑本脚本（线上实时流是有界滚动窗口）。"
  elif [ "${DESIGN_EXIT}" -ne 0 ]; then
    echo "✗ 设计对照断言失败（退出码 ${DESIGN_EXIT}）—— 这是**产品问题**，见上方逐条红。"
  fi
else
  echo "▶ 跳过页面内断言（未给 STAGE_TREE_CDP / STAGE_TREE_TOKEN_FILE）"
fi
