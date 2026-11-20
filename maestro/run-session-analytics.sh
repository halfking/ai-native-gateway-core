#!/usr/bin/env bash
# maestro/run-session-analytics.sh —— 会话分析系统 5 页的**实跑入口**（Maestro flow + CDP 探针）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 为什么是「两层」：                                                   │
# │   第一层 maestro/flows/16-session-analytics-system.yaml                  │
# │     ⇒ 证明「浏览器真的把这 5 个页面打开并渲染了」（元素在视口内、登录态成立）│
# │   第二层 maestro/tools/assert-session-analytics.mjs                      │
# │     ⇒ 证明「数字是对的」（宽表右侧列在内部滚动容器里、折叠线以下的内容   │
# │       Maestro 物理上够不着 ⇒ 只能页面内做 DOM 级断言 + 与 API 逐字对拍）│
# │   两层都绿才算「这 5 页在真实部署上可用」；只跑一层都会给出半个真相。     │
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   BASE_URL=https://llm.itestu.cn \
#   ADMIN_USER=admin ADMIN_PASSWORD='<口令>' \
#   bash maestro/run-session-analytics.sh
#
# 环境变量：
#   BASE_URL              管理后台地址（默认 https://llm.itestu.cn）
#   ADMIN_USER/ADMIN_PASSWORD  登录凭据（缺省从仓根 .env.local 读；
#                         211 的口令在仓内既有资产 scripts/maestro-simple-expert-mode-test.sh）
#   MAESTRO_DRIVER_PORT   Maestro 用的 CDP 端口（默认 9222）
#   SA_CDP_PORT           第二层探针用的 CDP 端口（默认 9223）
#   MAESTRO_SCREEN_SIZE   视口（默认 1440x1400；web 驱动固定 1024x625 ⇒ 必须覆写）
#   FLOW                  指定 flow（默认 maestro/flows/16-session-analytics-system.yaml）
#
# 产物：
#   maestro/reports/<时间戳>/16-session-analytics-system.log   Maestro CLI 输出
#   maestro/reports/<时间戳>/assert-session-analytics.txt      第二层探针输出
#   maestro/reports/<时间戳>/summary.txt                       两层汇总
#
# 退出码：0 = 两层全绿；1 = 有红；2 = 量具失效（窗口内无数据）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
SA_CDP_PORT="${SA_CDP_PORT:-9223}"
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x1400}"
FLOW="${FLOW:-maestro/flows/16-session-analytics-system.yaml}"
REPORT_ROOT="${ROOT}/maestro/reports"

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

command -v maestro >/dev/null 2>&1 || { echo "!! PATH 里没有 maestro（curl -Ls https://get.maestro.mobile.dev | bash）" >&2; exit 2; }

# ── 2. 凭据 ──────────────────────────────────────────────────────────────
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
: "${ADMIN_USER:?需要 ADMIN_USER（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_USER）}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_PASSWORD）}"

# ── 3. Chrome（两个独立 profile：Maestro 一个、第二层探针一个）────────────
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

start_chrome() {
  local port="$1" profile="$2"
  curl -s --max-time 3 "http://127.0.0.1:${port}/json/version" >/dev/null 2>&1 && return 0
  mkdir -p "${profile}"
  nohup "${CHROME_BIN}" --headless=new --remote-debugging-port="${port}" \
    --user-data-dir="${profile}" --no-first-run --no-default-browser-check \
    --disable-gpu --window-size=1440,1400 about:blank \
    >"${REPORT_ROOT}.chrome-${port}.log" 2>&1 &
  for _ in $(seq 1 30); do
    curl -s --max-time 2 "http://127.0.0.1:${port}/json/version" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "!! Chrome(:${port}) 起不来（看 ${REPORT_ROOT}.chrome-${port}.log）" >&2
  return 1
}

# ★ 用**全新 profile**：AGENTS.md 记过一个坑——遗留 Chrome 的旧 cookie 会与新 cookie
#   同名共存 ⇒ 服务端读到旧会话 ⇒ 页面数据源静默降级。
#   这里的两个 profile 都是本轮专用目录，不复用历史站点的 cookie 罐。
start_chrome "${DRIVER_PORT}" "/tmp/sa-maestro-chrome-${DRIVER_PORT}" || exit 2
start_chrome "${SA_CDP_PORT}" "/tmp/sa-probe-chrome-${SA_CDP_PORT}" || exit 2

# ── 4. 解析动态段 owner（人员详情页的真实 owner）─────────────────────────
# ★ 动态段必须喂真值：不喂真值 ⇒ 打开 "/admin/session-analytics/user-ranking/"
#   ⇒ 断言必然红、而红的原因与被测页面无关（与 run-local.sh 的 resolve-ids 同一理由）。
TOKEN="$(curl -s --max-time 20 -X POST "${BASE_URL}/api/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASSWORD}\"}" \
  | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('access_token') or d.get('token') or '')" 2>/dev/null)"
if [ -z "${TOKEN}" ]; then
  echo "!! 取 token 失败（${BASE_URL}/api/auth/token）" >&2
  exit 1
fi
END_DATE="$(date +%Y-%m-%d)"
START_DATE="$(date -v-6d +%Y-%m-%d 2>/dev/null || date -d '6 days ago' +%Y-%m-%d)"
SESSION_OWNER="$(curl -s --max-time 20 -H "Authorization: Bearer ${TOKEN}" \
  "${BASE_URL}/api/admin/session-analytics/user-ranking?start_date=${START_DATE}&end_date=${END_DATE}&limit=1" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
u=(d.get('users') or [])
print(u[0]['owner'] if u and u[0].get('owner') else '')
" 2>/dev/null)"
if [ -z "${SESSION_OWNER}" ]; then
  echo "!! 窗口 ${START_DATE}~${END_DATE} 内没有人员行 ⇒ 动态段无真值 ⇒ flow 会退化成空 URL。" >&2
  echo "   （这不是产品红，是量具失效；换窗口或先造数据。）" >&2
  exit 2
fi
echo "== 动态段 owner = ${SESSION_OWNER}（窗口 ${START_DATE}~${END_DATE}）"

# ── 5. 第一层：Maestro flow ─────────────────────────────────────────────
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="${REPORT_ROOT}/${STAMP}"
mkdir -p "${OUT}"
SUMMARY="${OUT}/summary.txt"
: >"${SUMMARY}"

echo "== 第一层：Maestro flow（${FLOW}）"
maestro test \
  --driver-host-port "${DRIVER_PORT}" \
  --headless --no-ansi \
  --screen-size "${SCREEN_SIZE}" \
  --test-output-dir "${OUT}/flow" \
  -e "BASE_URL=${BASE_URL}" \
  -e "ADMIN_USER=${ADMIN_USER}" \
  -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
  -e "SESSION_OWNER=${SESSION_OWNER}" \
  "${FLOW}" 2>&1 | tee "${OUT}/16-session-analytics-system.log"
FLOW_RC=${PIPESTATUS[0]}
FLOW_FAILED="$(grep -c 'FAILED' "${OUT}/16-session-analytics-system.log" || true)"

# ── 6. 第二层：CDP 探针 ─────────────────────────────────────────────────
echo "== 第二层：CDP 探针（assert-session-analytics.mjs）"
node "${ROOT}/maestro/tools/assert-session-analytics.mjs" \
  --cdp "http://127.0.0.1:${SA_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  --password "${ADMIN_PASSWORD}" \
  --start-date "${START_DATE}" \
  --end-date "${END_DATE}" \
  2>&1 | tee "${OUT}/assert-session-analytics.txt"
PROBE_RC=${PIPESTATUS[0]}

printf 'flow rc=%s FAILED=%s\n' "${FLOW_RC}" "${FLOW_FAILED}" >>"${SUMMARY}"
printf 'probe rc=%s\n' "${PROBE_RC}" >>"${SUMMARY}"
echo "---- 汇总 ----"
cat "${SUMMARY}"
echo "产物目录：${OUT}"

if [ "${FLOW_RC}" -ne 0 ] || [ "${PROBE_RC}" -ne 0 ]; then
  exit 1
fi
exit 0
