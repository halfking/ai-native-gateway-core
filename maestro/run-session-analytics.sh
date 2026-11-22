#!/usr/bin/env bash
# maestro/run-session-analytics.sh —— 会话分析系统的**实跑入口**（Maestro flow + 两层 CDP 探针）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 为什么是「三层」：                                                   │
# │   第一层 maestro/flows/16-session-analytics-system.yaml                  │
# │     ⇒ 证明「浏览器真的把这 5 个页面打开并渲染了」（元素在视口内、登录态成立）│
# │   第二层 maestro/tools/assert-session-analytics.mjs                      │
# │     ⇒ 证明「数字是对的」（宽表右侧列在内部滚动容器里、折叠线以下的内容   │
# │       Maestro 物理上够不着 ⇒ 只能页面内做 DOM 级断言 + 与 API 逐字对拍）│
# │   第三层 maestro/tools/assert-filter-button.mjs                          │
# │     ⇒ 证明「按钮的文案与行为一致」（3 个排行页的 .sa-btn--primary：文案是│
# │       「应用筛选」、点击后按新排序发请求、且**区间显示值不变** ⇒ 它不是  │
# │       区间开关；8238f6a3d 的键迁移修的正是这个语义错配）                 │
# │   三层都绿才算「这 5 页在真实部署上可用」；只跑一层都会给出半个真相。     │
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
#   MAESTRO_DRIVER_PORT   Maestro 用的 CDP 端口（默认 9222；**被占用则顺延**，绝不复用那个浏览器）
#   SA_CDP_PORT           第二层探针用的 CDP 端口（默认 9223；同上）
#   MAESTRO_SCREEN_SIZE   视口（默认 1440x1400；web 驱动固定 1024x625 ⇒ 必须覆写）
#   FLOW                  指定 flow（默认 maestro/flows/16-session-analytics-system.yaml）
#
# ★ 每次运行都是**干净起点**（2026-11-20 批判式审计修正）：
#   · 端口占用 ⇒ 换端口，不复用 ⇒ 不继承旧浏览器的 cookie / localStorage
#   · profile 目录带时间戳 ⇒ 每轮全新
#   · Chrome 带 `--lang=zh-CN` ⇒ 应用 locale（取自 navigator.language）被钉住，
#     断言里的中文判据（数据卡 label / 表头）不会因语种变化而静默失效
#
# 产物：
#   maestro/reports/<时间戳>/16-session-analytics-system.log   Maestro CLI 输出
#   maestro/reports/<时间戳>/assert-session-analytics.txt      第二层探针输出
#   maestro/reports/<时间戳>/assert-filter-button.txt          第三层探针输出
#   maestro/reports/<时间戳>/summary.txt                       两层汇总
#
# 退出码：0 = 三层全绿；1 = 有红；2 = 量具失效（窗口内无数据）。
# ★ 三态口径来自 `maestro/tools/result.mjs`（仓内判定口径的唯一真源）：
#   旧版本脚本把各层压平成「非 0 就 exit 1」⇒ **量具失效（2）永远出不来**
#   ⇒ 与仓内唯一真源的三态语义脱节（AGENTS.md ⑰：工具不得自己写一份退出码）。
#   合并优先级：**红 > 量具失效 > 绿**（红＝测了且不合格；量具失效＝没测到）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
SA_CDP_PORT="${SA_CDP_PORT:-9223}"
# ★ 2026-11-23（L15 真机复核轮）：视口从 **1440x1400** 提到 **1440x3600**。
#   理由（实测几何 + 实测截图，非拍脑袋）：
#     ① 上游 L7 轮把会话统计面板**加高** ⇒ 会话分析 users 页的锚点被整体推到
#        1400px 视口折叠线以下 —— 实测文档 Y：sa-top5-section=**2437**、
#        sa-range-label=**3223**、sa-users-col-rank=**3329**；
#     ② Maestro 的「看得见」只认**视口内**（flow 头注④：折叠线以下的内容断言不到）
#        ⇒ 旧视口下**必然假红**（实测 flow rc=1 FAILED=1，而 CDP 侧 t+2s 就已
#        渲染出这些元素 ⇒ **产品是好的**）；
#     ③ ★ **真旋钮是 Maestro 的 `--screen-size`（本变量）**，不是 Chrome 的
#        `--window-size` —— 误改 Chrome 窗口时实测截图仍是 1440x**1257**
#        （Maestro 自己按 `--screen-size` 设 device metrics）⇒ 记下来免得再踩。
#   ⇒ 3600 > 3329（最高锚点）⇒ 所有锚点进入视口。
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x3600}"
FLOW="${FLOW:-maestro/flows/16-session-analytics-system.yaml}"
REPORT_ROOT="${ROOT}/maestro/reports"
# ★ Chrome 日志写进 reports/ **内部** ⇒ 目录必须先建：新鲜 worktree/新 checkout 里
#   maestro/reports/ 不存在 ⇒ nohup 重定向失败 ⇒ Chrome 压根没起 ⇒ 假报「Chrome 起不来」
#   （2026-11-21 实测坑；run-local/run-mock-only 的 chrome.log 在 maestro/ 下兄弟路径、
#     run-users-usage 落 /dev/null ⇒ 那三处无此缺口。）
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
# ★ 变量名映射（2026-11-21 实测修复）：.env.local 里用的是**部署环境变量名**
#   LLM_GATEWAY_ADMIN_USER / LLM_GATEWAY_ADMIN_PASSWORD，而本 runner 认的是
#   ADMIN_USER / ADMIN_PASSWORD ⇒ 上面 source 成功也不会产生 ADMIN_* ⇒
#   凭据判定处必然退出 1（注释写着「缺省从仓根 .env.local 读」，实现没做到）。
#   优先级：显式传入的 ADMIN_* > .env.local 的 LLM_GATEWAY_ADMIN_*（谁先谁优先）。
ADMIN_USER="${ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-}}"
_pw_cur=ADMIN_PASSWORD
_pw_src=LLM_GATEWAY_ADMIN_PASSWORD
if [ -z "${!_pw_cur:-}" ]; then
  ADMIN_PASSWORD="${!_pw_src:-}"
fi
: "${ADMIN_USER:?需要 ADMIN_USER（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_USER）}"
: "${ADMIN_PASSWORD:?需要 ADMIN_PASSWORD（环境变量或仓根 .env.local 的 LLM_GATEWAY_ADMIN_PASSWORD）}"
# ★ 登录成功标记 = 顶栏 display_name（_shared/login.yaml 判据②③）：多账号时代它与账号绑定，
#   默认值放调用侧（README 纪律：flow env: 块压过 -e ⇒ 不能写进 flow）；
#   非默认账号（如 demoadmin=「演示管理员」）由调用方 -e 覆盖。
ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-系统管理员}"

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

# ★ 回收：本脚本起的 Chrome 跑完就杀 ⇒ 不留遗留浏览器占端口、不留旧 cookie 罐。
#   （实测踩过：9222 上挂着上一轮的常驻 Chrome ⇒ 端口被占 ⇒ 逼得端口顺延，
#     也正是「复用旧浏览器」那个坑的温床。）
CHROME_PIDS=()
cleanup() {
  local pid
  for pid in "${CHROME_PIDS[@]:-}"; do
    [ -n "${pid}" ] && kill "${pid}" 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

# ★ 端口空闲探测（不用 /dev/tcp：macOS bash 3.2 的 /dev/tcp 在部分环境下不可用，
#   这里统一走 curl —— 端口被 CDP 占用即可见）。
port_busy() {
  curl -s --max-time 2 "http://127.0.0.1:$1/json/version" >/dev/null 2>&1
}
# ★ **端口被占用就换端口，绝不复用那个浏览器**。
#   理由（AGENTS.md ④-1 的实测坑）：复用的浏览器会带着**上一轮的 cookie / localStorage**
#   ⇒ 旧的 `llmgw_locale`（界面语言）与旧会话 cookie 会静默生效
#   ⇒ 页面数据源降级、断言读到的不是被测状态。
#   ⇒ 换端口 + 全新 profile 才是「干净起点」。
# ★ `exclude` 参数**必须**用：两层要两个**互不相同**的浏览器。
#   （实测踩过：先给第一层顺延到 9223，第二层再探测时 9223 的 Chrome 还没起来
#     ⇒ 两层拿到了**同一个**端口 ⇒ 共用一个 profile ⇒ 设计失效。）
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
  # ★ profile 目录带时间戳 ⇒ 每轮全新 ⇒ 不继承任何 cookie / localStorage。
  mkdir -p "${profile}"
  # ★ `--lang=zh-CN`：应用 locale 取自 `navigator.language`（`web/src/i18n.ts:82`），
  #   且持久化在 `localStorage['llmgw_locale']` ⇒ 浏览器语言一变，整页文案就换语种
  #   ⇒ 断言的中文判据（数据卡 label / 表头 / 区块标题）会**静默失效**。
  #   这里从浏览器侧钉死语言，探针侧再读回 `<html lang>` 复核（双保险）。
  # ★ 2026-11-23（L15 真机复核轮）：视口高度从 1400 提到 **3600**。
  #   理由（实测几何，非拍脑袋）：上游 L7 轮把会话统计面板加高 ⇒ 会话分析 users 页
  #   的锚点被整体推到 1400px 视口折叠线以下 ——
  #     sa-top5-section=**2437**、sa-range-label=**3223**、sa-users-col-rank=**3329**
  #   ⇒ Maestro 的「看得见」只认视口内 ⇒ 断不到 ⇒ **量具假红**（产品侧 CDP 测得
  #   t+2s 即已渲染出这些元素 ⇒ 页面是好的）。
  #   ⇒ 两个候选修法：① `scrollUntilVisible` —— ★ **实测 web 驱动不支持滚动**
  #     （Maestro 直接报「No visible element found」）⇒ 弃用；
  #     ② **加高视口**（本 flow 头注早就写明「视口必须 …（run-local.sh 传入）
  #     ⇒ 折叠线以下的内容断言不到」⇒ 视口就是旋钮）⇒ 采用。
  #   ⇒ ★ 注意：**视口的真旋钮是 Maestro 的 `--screen-size`**（见 SCREEN_SIZE）
  #     —— Chrome 窗口这里只是"跟着对齐"，实测误改它而没改 --screen-size 时
  #     截图仍是 1440x1257 ⇒ Maestro 自己按 --screen-size 设 device metrics。
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

# ── 先起第一层的 Chrome，再挑第二层的端口 ────────────────────────────────
# ★ 顺序有讲究：先起第一层 ⇒ 第二层探测端口时第一层已占位 ⇒ 不会撞同一个端口。
start_chrome "${DRIVER_PORT}" "/tmp/sa-maestro-chrome-${STAMP}-${DRIVER_PORT}" || exit 2
SA_CDP_PORT="$(pick_free_port "${SA_CDP_PORT}" "${DRIVER_PORT}")" || { echo "!! ${SA_CDP_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }
start_chrome "${SA_CDP_PORT}" "/tmp/sa-probe-chrome-${STAMP}-${SA_CDP_PORT}" || exit 2
echo "== CDP 端口：Maestro=${DRIVER_PORT} 探针=${SA_CDP_PORT}（占用则顺延，保证是**全新且互不相同**的两个浏览器）"

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
# （STAMP 已在上面算好——端口分配要靠它做每轮唯一的 profile 名）
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
  -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME}" \
  -e "SESSION_OWNER=${SESSION_OWNER}" \
  "${FLOW}" 2>&1 | tee "${OUT}/16-session-analytics-system.log"
FLOW_RC=${PIPESTATUS[0]}
FLOW_FAILED="$(grep -c 'FAILED' "${OUT}/16-session-analytics-system.log" || true)"

# ── 6. 第二层：CDP 探针 ─────────────────────────────────────────────────
echo "== 第二层：CDP 探针（assert-session-analytics.mjs）"
# ★ 口令走**环境变量**而不是命令行参数（`--password` 会出现在 `ps` 输出里 ⇒ 同机可见）
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
node "${ROOT}/maestro/tools/assert-session-analytics.mjs" \
  --cdp "http://127.0.0.1:${SA_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  --start-date "${START_DATE}" \
  --end-date "${END_DATE}" \
  2>&1 | tee "${OUT}/assert-session-analytics.txt"
PROBE_RC=${PIPESTATUS[0]}

# ── 7. 第三层：CDP 探针（排序筛选按钮的语义断言）─────────────────────────
# ★ 为什么复用第二层的 Chrome（SA_CDP_PORT）：两个探针各自 Target.createTarget
#   开新 tab ⇒ 互不干扰；省掉第三个浏览器实例（起三个 Chrome 的内存与端口风险都不值）。
# ★ 覆盖对象：3 个排行页筛选栏的 .sa-btn--primary —— 文案（应用筛选）+ 行为
#   （点击后按新排序发请求、且**区间显示值不变** ⇒ 它不是区间开关）。
#   这正是 8238f6a3d 键迁移的交付物；既有两层都不覆盖它 ⇒ 单独立一条。
echo "== 第三层：CDP 探针（assert-filter-button.mjs）"
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
node "${ROOT}/maestro/tools/assert-filter-button.mjs" \
  --cdp "http://127.0.0.1:${SA_CDP_PORT}" \
  --url "${BASE_URL}" \
  --start-date "${START_DATE}" \
  --end-date "${END_DATE}" \
  2>&1 | tee "${OUT}/assert-filter-button.txt"
FILTER_RC=${PIPESTATUS[0]}

printf 'filter rc=%s\n' "${FILTER_RC}" >>"${SUMMARY}"
printf 'flow rc=%s FAILED=%s\n' "${FLOW_RC}" "${FLOW_FAILED}" >>"${SUMMARY}"
printf 'probe rc=%s\n' "${PROBE_RC}" >>"${SUMMARY}"
echo "---- 汇总 ----"
cat "${SUMMARY}"
echo "产物目录：${OUT}"

# ── 判定：★ 三态口径（0=绿 / 1=红 / 2=量具失效），与 maestro/tools/result.mjs 一致 ──
# ★ 旧版是「任一层非 0 ⇒ exit 1」⇒ 量具失效（2）被压平成红 ⇒ 上游拿到的信号是错的
#   （「没测到」被当成「不合格」）。现在按层语义合并：
#   · 任一层 rc=1 ⇒ **产品红**（已评估且不合格）
#   · 无红、但有层 rc=2 ⇒ 量具失效（窗口内无数据 ⇒ 断言未评估）
#   · 全 0 ⇒ 绿
#   ⇒ 优先级：**红 > 量具失效 > 绿**（红＝测了且不合格；量具失效＝没测到）
if [ "${FLOW_RC}" -ne 0 ]; then
  echo "VERDICT: FAIL（第一层 flow 红：rc=${FLOW_RC} FAILED=${FLOW_FAILED}）"
  exit 1
fi
if [ "${PROBE_RC}" -eq 1 ]; then
  echo "VERDICT: FAIL（第二层探针红：rc=${PROBE_RC}）"
  exit 1
fi
if [ "${FILTER_RC}" -eq 1 ]; then
  echo "VERDICT: FAIL（第三层探针红：rc=${FILTER_RC}）"
  exit 1
fi
if [ "${PROBE_RC}" -eq 2 ] || [ "${FILTER_RC}" -eq 2 ]; then
  echo "VERDICT: 量具失效（探针 rc=2：断言未评估，**这不是产品缺陷**）"
  exit 2
fi
echo "VERDICT: PASS（三层全绿）"
exit 0