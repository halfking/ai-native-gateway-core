#!/usr/bin/env bash
# maestro/run-stats-range-picker.sh —— 「时间范围统一 + 统计说明提示符」的**实跑入口**
#   （Maestro flow 第一层 + CDP 探针第二层）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 为什么是「两层」（与 run-session-analytics.sh 的三层分工同构）：        │
# │   第一层 maestro/flows/18-stats-range-and-scope-hint.yaml               │
# │     ⇒ 证明「浏览器真的能打开这三页、点到那个元素、面板真的展开」        │
# │       （结构判据 = data-testid ⇒ 与界面语言解耦）                        │
# │   第二层 maestro/tools/assert-stats-range-picker.mjs                    │
# │     ⇒ 证明「语义是对的」：                                            │
# │       · 三页的固定档位**逐字同序相等**（真的是同一个组件 ⇒ 同一套预设） │
# │       · `.kx-dr-inputs` 渲染出来了（自定义起止没被禁）                  │
# │       · 提示符**点击前正文不可见、点击后可见**（不是常驻横幅）           │
# │       · 提示符与刷新按钮**同容器**（真的在刷新数据旁）                   │
# │   ★ 只跑一层都会给出半个真相（结构对 ≠ 语义对 / 语义对 ≠ 浏览器能点到）。│
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   BASE_URL=https://llm.itestu.cn \
#   bash maestro/run-stats-range-picker.sh
#
# 环境变量：
#   BASE_URL              管理后台地址（默认 https://llm.itestu.cn）
#   ADMIN_USER/ADMIN_PASSWORD  登录凭据（缺省从仓根 .env.local 读）
#   MAESTRO_DRIVER_PORT   Maestro 用的 CDP 端口（默认 9222；**被占用则顺延**）
#   PROBE_CDP_PORT        第二层探针用的 CDP 端口（默认 9223；同上）
#   MAESTRO_SCREEN_SIZE   视口（默认 1440x3600）
#   FLOW                  指定 flow（默认 maestro/flows/18-stats-range-and-scope-hint.yaml）
#
# ★ 每轮都是**干净起点**：端口占用就换端口（不复用旧浏览器 ⇒ 不继承旧 cookie /
#   localStorage）、profile 目录带时间戳、Chrome 带 `--lang=zh-CN`。
# ★ 退出码：0 = 两层全绿；1 = 有红；2 = 量具失效。
#   口径来自 maestro/tools/result.mjs（仓内判定口径的唯一真源）；
#   合并优先级：**红 > 量具失效 > 绿**。
#
# ★ 关于「端口/JDK/Chrome 这段为什么和 run-session-analytics.sh 长得一样」：
#   ★ 这是**有意的重复**——仓内多个 runner（run-local.sh / run-users-usage.sh /
#     run-stage-tree.sh）各自持有一份同样形态的实现。
#   ★ 抽共享库的风险：改一处会同时影响**已在跑的**另一条链（会话分析三层链）；
#     而本 runner 只服务本轮这一条链 ⇒ **局部复制比跨链耦合安全**。
#   ⇒ 若日后要收敛，先让两条链都跑绿再抽（收敛本身要单独一轮）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
PROBE_CDP_PORT="${PROBE_CDP_PORT:-9223}"
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x3600}"
FLOW="${FLOW:-maestro/flows/18-stats-range-and-scope-hint.yaml}"
REPORT_ROOT="${ROOT}/maestro/reports"
mkdir -p "${REPORT_ROOT}"

# ── 1. JDK ────────────────────────────────────────────────────────────────
find_jdk() {
  local c
  for c in "${MAESTRO_JAVA_HOME:-}" "${JAVA_HOME:-}" "${HOME}/sdk/bash5/bin"; do
    [ -n "$c" ] && [ -x "$c/bin/java" ] && { printf '%s' "$c"; return; }
  done
  if command -v /usr/libexec/java_home >/dev/null 2>&1; then
    c="$(/usr/libexec/java_home -v 17 2>/dev/null || true)"
    [ -n "$c" ] && [ -x "$c/bin/java" ] && { printf '%s' "$c"; return; }
  fi
  for c in /opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
           /usr/local/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
           "${HOME}"/jdk*/Contents/Home \
           /Library/Java/JavaVirtualMachines/*/Contents/Home; do
    [ -x "$c/bin/java" ] && { printf '%s' "$c"; return; }
  done
  printf ''
}
JAVA_HOME_CANDIDATE="$(find_jdk)"
if [ -z "${JAVA_HOME_CANDIDATE}" ]; then
  echo "!! 找不到 JDK。设 MAESTRO_JAVA_HOME=<jdk 根>。" >&2
  exit 2
fi
export JAVA_HOME="${JAVA_HOME_CANDIDATE}"
export PATH="${JAVA_HOME}/bin:${PATH}"

command -v maestro >/dev/null 2>&1 || { echo "!! PATH 里没有 maestro" >&2; exit 2; }

# ── 2. 凭据（.env.local 里是 LLM_GATEWAY_ADMIN_* ⇒ 要映射到 ADMIN_*）────────
if [ -z "${ADMIN_USER:-}" ] || [ -z "${ADMIN_PASSWORD:-}" ]; then
  for f in "${ROOT}/.env.local" "${ROOT}/../llm-gateway-go/.env.local"; do
    [ -f "$f" ] || continue
    set -a
    # shellcheck disable=SC1090
    . "$f"
    set +a
    break
  done
fi
ADMIN_USER="${ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-}}"
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  ADMIN_PASSWORD="${LLM_GATEWAY_ADMIN_PASSWORD:-}"
fi
: "${ADMIN_USER:?需要 ADMIN_USER（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_USER）}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_PASSWORD）}"
ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-系统管理员}"

# ── 3. Chrome（两个独立 profile）──────────────────────────────────────────
find_chrome() {
  if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
  command -v chromium >/dev/null 2>&1 && { command -v chromium; return; }
  command -v google-chrome >/dev/null 2>&1 && { command -v google-chrome; return; }
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium" \
           "/usr/bin/google-chrome" "/usr/bin/chromium-browser"; do
    [ -x "$c" ] && { printf '%s' "$c"; return; }
  done
  printf ''
}
CHROME_BIN="$(find_chrome)"
[ -n "${CHROME_BIN}" ] || { echo "!! 找不到 Chrome。" >&2; exit 2; }

CHROME_PIDS=()
cleanup() {
  local pid
  for pid in "${CHROME_PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

port_busy() {
  curl -s --max-time 2 "http://127.0.0.1:$1/json/version" >/dev/null 2>&1
}
# ★ 端口被占用就换端口，**绝不复用**那个浏览器（会带着上一轮的 cookie/localStorage）。
pick_free_port() {
  local base="$1" exclude="${2:-}" p
  for p in $(seq "$base" $((base + 20))); do
    [ -n "${exclude}" ] && [ "${p}" = "${exclude}" ] && continue
    if ! port_busy "$p"; then printf '%s' "$p"; return 0; fi
  done
  return 1
}

STAMP="$(date +%Y%m%d-%H%M%S)"
DRIVER_PORT="$(pick_free_port "${DRIVER_PORT}")" || { echo "!! ${DRIVER_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }

start_chrome() {
  local port="$1" profile="$2"
  mkdir -p "${profile}"
  nohup "${CHROME_BIN}" --headless=new --remote-debugging-port="${port}" \
    --user-data-dir="${profile}" --no-first-run --no-default-browser-check \
    --disable-gpu --lang=zh-CN --window-size="${SA_WINDOW_SIZE:-1440,3600}" about:blank \
    >"${REPORT_ROOT}/.chrome-${port}.log" 2>&1 &
  CHROME_PIDS+=("$!")
  for _ in $(seq 1 30); do
    port_busy "${port}" && return 0
    sleep 1
  done
  echo "!! Chrome(:${port}) 起不来（看 ${REPORT_ROOT}/.chrome-${port}.log）" >&2
  return 1
}

# ★ 先起第一层的 Chrome，再挑第二层的端口（顺序 ⇒ 两层不会撞同一个端口）。
start_chrome "${DRIVER_PORT}" "/tmp/srp-maestro-chrome-${STAMP}-${DRIVER_PORT}" || exit 2
PROBE_CDP_PORT="$(pick_free_port "${PROBE_CDP_PORT}" "${DRIVER_PORT}")" || { echo "!! ${PROBE_CDP_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }
start_chrome "${PROBE_CDP_PORT}" "/tmp/srp-probe-chrome-${STAMP}-${PROBE_CDP_PORT}" || exit 2
echo "== CDP 端口：Maestro=${DRIVER_PORT} 探针=${PROBE_CDP_PORT}（占用则顺延，保证是全新且互不相同的两个浏览器）"

OUT="${REPORT_ROOT}/${STAMP}"
mkdir -p "${OUT}"
SUMMARY="${OUT}/summary.txt"
: >"${SUMMARY}"

# ── 4. 第一层：Maestro flow ───────────────────────────────────────────────
echo "== 第一层：Maestro flow（${FLOW}）"
maestro test \
  --driver-host-port "${DRIVER_PORT}" \
  --headless --no-ansi \
  --screen-size "${SCREEN_SIZE}" \
  --test-output-dir "${OUT}/flow" \
  -e "BASE_URL=${BASE_URL}" \
  -e "ADMIN_USER=${ADMIN_USER}" \
  -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
  -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME}" \
  "${FLOW}" 2>&1 | tee "${OUT}/18-stats-range-and-scope-hint.log"
FLOW_RC=${PIPESTATUS[0]}
FLOW_FAILED="$(grep -c 'FAILED' "${OUT}/18-stats-range-and-scope-hint.log" || true)"

# ── 5. 第二层：CDP 探针 ───────────────────────────────────────────────────
echo "== 第二层：CDP 探针（assert-stats-range-picker.mjs）"
# ★ 口令走环境变量，不进命令行（`--password` 会出现在 `ps` 输出里 ⇒ 同机可见）。
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
node "${ROOT}/maestro/tools/assert-stats-range-picker.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  2>&1 | tee "${OUT}/assert-stats-range-picker.txt"
PROBE_RC=${PIPESTATUS[0]}

printf 'flow rc=%s FAILED=%s\n' "${FLOW_RC}" "${FLOW_FAILED}" >>"${SUMMARY}"
printf 'probe rc=%s\n' "${PROBE_RC}" >>"${SUMMARY}"
echo "---- 汇总 ----"
cat "${SUMMARY}"
echo "产物目录：${OUT}"

# ── 判定：★ 三态（0=绿 / 1=红 / 2=量具失效），优先级 **红 > 量具失效 > 绿** ──
if [ "${FLOW_RC}" -ne 0 ]; then
  echo "VERDICT: FAIL（第一层 flow 红：rc=${FLOW_RC} FAILED=${FLOW_FAILED}）"
  exit 1
fi
if [ "${PROBE_RC}" -eq 1 ]; then
  echo "VERDICT: FAIL（第二层探针红：rc=${PROBE_RC}）"
  exit 1
fi
if [ "${PROBE_RC}" -eq 2 ]; then
  echo "VERDICT: 量具失效（探针 rc=2：断言未评估，**这不是产品缺陷**）"
  exit 2
fi
echo "VERDICT: PASS（两层全绿）"
exit 0
