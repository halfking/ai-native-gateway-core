#!/usr/bin/env bash
# run-users-usage.sh — 会话分析 users 页双层验证（2026-11-20 数字出处轮）
#
# 第一层：maestro flow 13（行为层：登录 → 列表身份/五列 → 帮助抽屉 → 行按钮下钻 → 详情首屏）
# 第二层：CDP 深度断言（折叠线下的模型明细卡/展开行矩阵/帮助抽屉五段——
#         应用在内部容器滚，Maestro 的 scrollUntilVisible 物理上到不了，见 12-flow 同款分工）
#
# 用法:
#   BASE_URL=https://llm.itestu.cn bash maestro/run-users-usage.sh
#   BASE_URL=http://127.0.0.1:8782 bash maestro/run-users-usage.sh
#
# 退出码：0 = 两层全绿；1 = 产品红；2 = 量具失效（第二层报 2 时透传）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x1400}"

# JDK（与 run-local.sh 同源探测）
find_jdk() {
  local c
  for c in "${MAESTRO_JAVA_HOME:-}" "${JAVA_HOME:-}"; do
    [ -n "${c}" ] && [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  done
  if command -v /usr/libexec/java_home >/dev/null 2>&1; then
    c="$(/usr/libexec/java_home -v 17 2>/dev/null || true)"
    [ -n "${c}" ] && [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  fi
  printf ''
}
JAVA_HOME_CANDIDATE="$(find_jdk)"
[ -z "${JAVA_HOME_CANDIDATE}" ] && { echo "!! 找不到 JDK 17" >&2; exit 2; }
export JAVA_HOME="${JAVA_HOME_CANDIDATE}"
export PATH="${JAVA_HOME}/bin:${PATH}"

if [ -z "${MAESTRO_ADMIN_USER:-}" ] || [ -z "${MAESTRO_ADMIN_PASSWORD:-}" ]; then
  if [ -f "${ROOT}/.env.local" ]; then
    # shellcheck disable=SC1091
    source "${ROOT}/.env.local"
  fi
fi
ADMIN_USER="${MAESTRO_ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:?missing}}"
ADMIN_PASSWORD="${MAESTRO_ADMIN_PASSWORD:-${LLM_GATEWAY_ADMIN_PASSWORD:?missing}}"

ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-系统管理员}"

OUT_DIR="maestro/reports/$(date +%Y%m%d-%H%M%S)-users-usage"
mkdir -p "${OUT_DIR}"
echo "▶ 输出目录：${OUT_DIR}"
echo "▶ BASE_URL=${BASE_URL} 视口=${SCREEN_SIZE}"

# ── 第一层：flow 13 ────────────────────────────────────────────────────────
set +e
maestro test \
  --headless \
  --no-ansi \
  --screen-size "${SCREEN_SIZE}" \
  --test-output-dir "${OUT_DIR}/flow13" \
  -e BASE_URL="${BASE_URL}" \
  -e ADMIN_USER="${ADMIN_USER}" \
  -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
  -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME}" \
  "${ROOT}/maestro/flows/13-session-analytics-users.yaml" 2>&1 | tee "${OUT_DIR}/flow13.log"
F13_EXIT=${PIPESTATUS[0]}
set -e
echo "▶ flow13 rc=${F13_EXIT}"

# ── 第二层：CDP 深度断言（独立 headless Chrome + API 登录）────────────────
CDP_PORT="${USAGE_CDP_PORT:-9223}"
CHROME_BIN="${MAESTRO_CHROME_BIN:-}"
if [ -z "${CHROME_BIN}" ]; then
  for c in chromium google-chrome "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; do
    command -v "${c}" >/dev/null 2>&1 && { CHROME_BIN="${c}"; break; }
  done
  [ -z "${CHROME_BIN}" ] && [ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ] && \
    CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
fi
[ -z "${CHROME_BIN}" ] && { echo "!! 找不到 Chrome/Chromium（设 MAESTRO_CHROME_BIN）" >&2; exit 2; }

CHROME_PROFILE="$(mktemp -d /tmp/lgw-users-usage-chrome.XXXXXX)"
"${CHROME_BIN}" --headless=new --remote-debugging-port="${CDP_PORT}" \
  --user-data-dir="${CHROME_PROFILE}" --no-first-run --no-default-browser-check \
  about:blank >/dev/null 2>&1 &
CHROME_PID=$!
trap 'kill "${CHROME_PID}" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do
  curl -s "http://127.0.0.1:${CDP_PORT}/json/version" >/dev/null 2>&1 && break
  sleep 0.5
done

set +e
ADMIN_PASSWORD="${ADMIN_PASSWORD}" node "${ROOT}/maestro/tools/assert-users-usage.mjs" \
  --cdp "http://127.0.0.1:${CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" 2>&1 | tee "${OUT_DIR}/cdp-assert.log"
CDP_EXIT=${PIPESTATUS[0]}
set -e
echo "▶ CDP 断言 rc=${CDP_EXIT}"

echo "==== 汇总 ===="
echo "flow13      rc=${F13_EXIT}（0=绿）"
echo "cdp-assert  rc=${CDP_EXIT}（0=绿 / 1=产品红 / 2=量具失效）"
[ "${F13_EXIT}" -eq 0 ] || exit 1
exit "${CDP_EXIT}"
