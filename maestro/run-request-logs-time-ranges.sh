#!/usr/bin/env bash
# maestro/run-request-logs-time-ranges.sh —— 请求日志页「切换时间范围后数据正确 ＋
#   查询不超时 ＋ 加载态可见」的**实跑入口**（Maestro flow 第一层 + CDP 探针第二层）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 为什么是「两层」（与 run-stats-range-picker.sh 同构）：                  │
# │   第一层 maestro/flows/19-request-logs-time-ranges.yaml                  │
# │     ⇒ 证明「真实浏览器能点到每个档位、点完页面处在正确状态」              │
# │       （加载收尾 / 有数据 / 无报错 / 连续切换停在最后选的档 / 含探测 / 翻页）│
# │   第二层 maestro/tools/assert-request-logs-time-ranges.mjs               │
# │     ⇒ 证明「语义是对的」：请求窗口 == 档位窗口、响应窗口 == 请求窗口（没有被 │
# │       静默截断）、界面 == 响应、被取代的请求被中止、加载态可见有读数、       │
# │       多条件矩阵（窗口 × 口径 × 状态 × 页码）逐条成立。                     │
# │   ★ 只跑一层都会给出半个真相（点得到 ≠ 数据对 / 数据对 ≠ 浏览器点得到）。 │
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   BASE_URL=https://llm.itestu.cn bash maestro/run-request-logs-time-ranges.sh
#
# 环境变量：
#   BASE_URL              管理后台地址（默认 https://llm.itestu.cn）
#   ADMIN_USER/ADMIN_PASSWORD  登录凭据（缺省从仓根 .env.local 读）
#     ★ 口令不是硬必需：默认走「CDP 预置会话」，不需要口令；
#       只有「预置失败且必须退回 UI 登录」时才需要它。
#   SEED_ENABLE           是否走 CDP 预置会话（默认 1；=0 ⇒ 退回纯 UI 登录路径）
#   SEED_SSH_HOST         现签 JWT 的机器（默认 211；密钥**不出这台机器**）
#   SEED_TOKEN_FILE       已备好的 JWT 文件（给了就不现签；**调用方自己负责删**）
#   SEED_MODE             注入的工作模式（默认 expert）
#   SEED_TTL              现签令牌的 TTL 秒数（默认 1800）
#   MAESTRO_DRIVER_PORT   Maestro 用的 CDP 端口（默认 9222；**被占用则顺延**）
#   PROBE_CDP_PORT        第二层探针用的 CDP 端口（默认 9223；同上）
#   MAESTRO_SCREEN_SIZE   视口（默认 1440x2200——筛选条头部在桌面视口）
#   FLOW                  指定 flow（默认 maestro/flows/19-request-logs-time-ranges.yaml）
#   MATRIX_ROUNDS         第二层多条件矩阵的轮数（默认 1；「反复测试」时调大）
#   HEAVY                 1 = 矩阵含近 14 / 30 天的重窗口（默认）；0 = 跳过
#   TODAY_PRESET_ID       覆盖「今天」档位（默认按 API 自动判定：今天没请求就换「昨天」）
#
# ★★ 为什么默认走「预置会话」（2026-11-25 captcha 死锁根治轮，见 README §6.4/§6.5.1）：
#   后端的登录失败计数是**纯内存、无 TTL**，captcha 闸门挂在任何 DB 查询与 bcrypt
#   之前 ⇒ 连续失败到阈值后，任何走 UI 登录的自动化都会永久变红。
#   ⇒ 用容器里的 LLM_GATEWAY_SECRET_KEY 在 211 本机现签一枚短期 JWT，再用 CDP 写进
#     浏览器的 HttpOnly cookie ⇒ 全程不碰 /api/auth/token。
#   ★ 签名密钥**不出 211**；令牌落 0600 临时文件，**跑完即删**（trap 负责）。
#
# ★ 每轮都是**干净起点**：端口占用就换端口、profile 目录带时间戳、Chrome 带 --lang=zh-CN。
# ★ 退出码：0 = 两层全绿；1 = 有红；2 = 量具失效。口径来自 maestro/tools/result.mjs。
#
# ★ 关于「这一段为什么和 run-stats-range-picker.sh 长得一样」：有意的局部复制
#   （仓内多个 runner 各持一份同形态实现；抽共享库会耦合已在跑的链）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
PROBE_CDP_PORT="${PROBE_CDP_PORT:-9223}"
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x2200}"
FLOW="${FLOW:-maestro/flows/19-request-logs-time-ranges.yaml}"
MATRIX_ROUNDS="${MATRIX_ROUNDS:-1}"
HEAVY="${HEAVY:-1}"
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
ADMIN_USER="${ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-admin}}"
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  ADMIN_PASSWORD="${LLM_GATEWAY_ADMIN_PASSWORD:-}"
fi
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
TOKEN_FILE=""
TOKEN_FILE_OWNED=0
cleanup() {
  local pid
  for pid in "${CHROME_PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  if [ "${TOKEN_FILE_OWNED}" = "1" ] && [ -n "${TOKEN_FILE}" ] && [ -f "${TOKEN_FILE}" ]; then
    rm -f "${TOKEN_FILE}"
  fi
}
trap cleanup EXIT INT TERM

port_busy() {
  curl -s --max-time 2 "http://127.0.0.1:$1/json/version" >/dev/null 2>&1
}
# ★ 端口被占用就换端口，**绝不复用**那个浏览器（会带着上一轮的 cookie/localStorage）。
pick_free_port() {
  local base="$1" exclude="${2:-}" p
  for p in $(seq "$base" $((base + 20))); do
    [ -n "${exclude}" ] && [ "$p" = "${exclude}" ] && continue
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
    --disable-gpu --lang=zh-CN --window-size="${SA_WINDOW_SIZE:-1440,2200}" about:blank \
    >"${REPORT_ROOT}/.chrome-${port}.log" 2>&1 &
  CHROME_PIDS+=("$!")
  for _ in $(seq 1 30); do
    port_busy "${port}" && return 0
    sleep 1
  done
  echo "!! Chrome(:${port}) 起不来（看 ${REPORT_ROOT}/.chrome-${port}.log）" >&2
  return 1
}

start_chrome "${DRIVER_PORT}" "/tmp/rltr-maestro-chrome-${STAMP}-${DRIVER_PORT}" || exit 2
PROBE_CDP_PORT="$(pick_free_port "${PROBE_CDP_PORT}" "${DRIVER_PORT}")" || { echo "!! ${PROBE_CDP_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }
start_chrome "${PROBE_CDP_PORT}" "/tmp/rltr-probe-chrome-${STAMP}-${PROBE_CDP_PORT}" || exit 2
echo "== CDP 端口：Maestro=${DRIVER_PORT} 探针=${PROBE_CDP_PORT}（占用则顺延，保证是全新且互不相同的两个浏览器）"

OUT="${REPORT_ROOT}/${STAMP}"
mkdir -p "${OUT}"
SUMMARY="${OUT}/summary.txt"
: >"${SUMMARY}"

# ── 3.5 预置会话（CDP seed-session）★ captcha 死锁的根治手段 ─────────────
run_with_timeout() {
  local secs="$1"; shift
  "$@" &
  local pid=$! watchdog rc
  ( sleep "${secs}"; kill -9 "${pid}" 2>/dev/null ) &
  watchdog=$!
  wait "${pid}"
  rc=$?
  kill "${watchdog}" 2>/dev/null
  wait "${watchdog}" 2>/dev/null
  return ${rc}
}

SEED_ENABLE="${SEED_ENABLE:-1}"
SEED_SSH_HOST="${SEED_SSH_HOST:-211}"
SEED_MODE="${SEED_MODE:-expert}"
SEED_TTL="${SEED_TTL:-1800}"
SEED_HELPER="${ROOT}/scripts/audit/api-211-admin-probe.py"

seed_mint() {
  local host="$1" ttl="$2" out="$3" py
  [ -f "${SEED_HELPER}" ] || { echo "!! 找不到 ${SEED_HELPER}" >&2; return 1; }
  scp -q -o ConnectTimeout=10 "${SEED_HELPER}" "${host}:/tmp/api-211-admin-probe.py" 2>/dev/null || return 1
  # ★ 脚本体走 stdin 传给远端 python3 -；远端 stdout 只有令牌本身 ⇒ 直接进 0600 文件。
  py="import importlib.util,sys
spec=importlib.util.spec_from_file_location('p','/tmp/api-211-admin-probe.py')
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
sys.stdout.write(m.mint_token(ttl=${ttl}))"
  printf '%s' "${py}" | ssh -o ConnectTimeout=10 "${host}" 'python3 -' >"${out}" 2>/dev/null || return 1
  [ -s "${out}" ] || return 1
  return 0
}

SEED_STATE="off"
if [ "${SEED_ENABLE}" = "1" ]; then
  if [ -n "${SEED_TOKEN_FILE:-}" ] && [ -s "${SEED_TOKEN_FILE}" ]; then
    TOKEN_FILE="${SEED_TOKEN_FILE}"
    TOKEN_FILE_OWNED=0
    echo "== 预置会话：沿用调用方给的令牌文件（${TOKEN_FILE}；本脚本不删它）"
  else
    TOKEN_FILE="$(mktemp "${TMPDIR:-/tmp}/llmgw-jwt.XXXXXX")"
    chmod 600 "${TOKEN_FILE}"
    TOKEN_FILE_OWNED=1
    echo "== 预置会话：在 ${SEED_SSH_HOST} 本机现签 JWT（ttl=${SEED_TTL}s，密钥不出机器）"
    if seed_mint "${SEED_SSH_HOST}" "${SEED_TTL}" "${TOKEN_FILE}"; then
      echo "== 预置会话：现签完成（令牌落 0600 临时文件，跑完自动删）"
    else
      echo "!! 现签失败 ⇒ 退回 UI 登录路径（若已处于 captcha 闸门状态则本轮必红）" >&2
      rm -f "${TOKEN_FILE}"
      TOKEN_FILE=""
      TOKEN_FILE_OWNED=0
    fi
  fi

  if [ -n "${TOKEN_FILE}" ]; then
    # 先验令牌：/api/auth/me 必须 200（只取状态码，不打响应体）。
    SEED_ME_RC="$(curl -s -m 15 -o /dev/null -w '%{http_code}' \
      -H "Cookie: llmgw_session=$(cat "${TOKEN_FILE}")" "${BASE_URL}/api/auth/me" 2>/dev/null || echo 000)"
    if [ "${SEED_ME_RC}" != "200" ]; then
      echo "!! 令牌未被后端认（/api/auth/me=${SEED_ME_RC}）⇒ 不注入" >&2
      rm -f "${TOKEN_FILE}"
      TOKEN_FILE=""
      TOKEN_FILE_OWNED=0
    else
      SEED_STATE="ok"
      if run_with_timeout 60 node "${ROOT}/maestro/tools/seed-session.mjs" \
        --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
        --url "${BASE_URL}" \
        --token-file "${TOKEN_FILE}" \
        --mode "${SEED_MODE}" \
        >"${OUT}/seed-session-${PROBE_CDP_PORT}.txt" 2>&1; then
        echo "== 预置会话：CDP :${PROBE_CDP_PORT}（探针）已注入（mode=${SEED_MODE}）"
      else
        echo "!! 预置会话：CDP :${PROBE_CDP_PORT} 注入失败（见 ${OUT}/seed-session-${PROBE_CDP_PORT}.txt）" >&2
        SEED_STATE="partial"
      fi
    fi
  fi
fi

if [ "${SEED_STATE}" != "ok" ] && [ -z "${ADMIN_PASSWORD}" ]; then
  echo "!! 既没预置成功、又没管理员口令 ⇒ 本轮无法建立会话（量具失效，不是产品缺陷）" >&2
  echo "!! 预置失败原因见上面日志；口令可用 ADMIN_PASSWORD=<...> 提供。" >&2
  exit 2
fi
printf 'seed-state=%s\n' "${SEED_STATE}" >>"${SUMMARY}"

# ── 3.6 「今天」有没有数据（决定 flow 的 TODAY_PRESET_ID）──────────────────
# ★ flow 断言「有数据」。凌晨整点后「今天」可能确实还没有请求 ⇒ 那是预期内的空集，
#   不是缺陷 ⇒ 这里按 API 实况把该档换成「昨天」（调用方可用 TODAY_PRESET_ID 覆盖）。
compute_today_preset() {
  local from to body count
  [ -n "${TOKEN_FILE}" ] || { printf 'kx-dr-preset-today'; return; }
  from="$(TZ=Asia/Shanghai date +%Y-%m-%dT00:00:00%z | sed -E 's/([+-][0-9]{2})([0-9]{2})$/\1:\2/')"
  to="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  body="$(curl -s -m 120 -H "Cookie: llmgw_session=$(cat "${TOKEN_FILE}")" \
    --get --data-urlencode "from=${from}" --data-urlencode "to=${to}" --data-urlencode "page_size=1" \
    "${BASE_URL}/api/logs" 2>/dev/null || true)"
  count="$(printf '%s' "${body}" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(String(JSON.parse(s).count))}catch{process.stdout.write("NaN")}})' 2>/dev/null || echo NaN)"
  if [ "${count}" != "NaN" ] && [ "${count}" -gt 0 ] 2>/dev/null; then
    printf 'kx-dr-preset-today'
  else
    printf 'kx-dr-preset-yesterday'
  fi
}
TODAY_PRESET_ID="${TODAY_PRESET_ID:-$(compute_today_preset)}"
echo "== 「今天」档位：${TODAY_PRESET_ID}（今天无请求时自动换成「昨天」）"
printf 'today-preset=%s\n' "${TODAY_PRESET_ID}" >>"${SUMMARY}"

# ── 4. 第一层：Maestro flow ───────────────────────────────────────────────
# ★ Maestro 的 web 驱动自起 Chrome（`--driver-host-port` 对 web 无效）⇒ 想让 flow
#   跑在预置会话上，必须往 **Maestro 自己的**浏览器里注入 ⇒ maestro 放后台，注入后再等它。
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
  -e "TODAY_PRESET_ID=${TODAY_PRESET_ID}" \
  "${FLOW}" >"${OUT}/19-request-logs-time-ranges.log" 2>&1 &
MAESTRO_PID=$!

maestro_chrome_profile() {
  local pid="$1" drv chrome args dir
  for drv in $(pgrep -P "${pid}" 2>/dev/null); do
    ps -p "${drv}" -o args= 2>/dev/null | grep -q "chromedriver" || continue
    for chrome in $(pgrep -P "${drv}" 2>/dev/null); do
      args="$(ps -p "${chrome}" -o args= 2>/dev/null)"
      case "${args}" in
        *--user-data-dir=*)
          dir="${args#*--user-data-dir=}"
          dir="${dir%% *}"
          printf '%s' "${dir}"
          return 0
          ;;
      esac
    done
  done
  return 1
}

maestro_cdp_port() {
  local pid="$1" profile port i
  for i in $(seq 1 60); do
    profile="$(maestro_chrome_profile "${pid}" 2>/dev/null || true)"
    if [ -n "${profile}" ] && [ -f "${profile}/DevToolsActivePort" ]; then
      port="$(head -1 "${profile}/DevToolsActivePort" 2>/dev/null || true)"
      if [ -n "${port}" ]; then printf '%s' "${port}"; return 0; fi
    fi
    kill -0 "${pid}" 2>/dev/null || return 1
    sleep 0.5
  done
  return 1
}

SEED_MAESTRO="no"
if [ -n "${TOKEN_FILE}" ]; then
  echo "== 预置会话：等 Maestro 的 Chrome 起来（往它自己的浏览器注入）"
  if MAESTRO_CDP="$(maestro_cdp_port "${MAESTRO_PID}")"; then
    if run_with_timeout 60 node "${ROOT}/maestro/tools/seed-session.mjs" \
      --cdp "http://127.0.0.1:${MAESTRO_CDP}" \
      --url "${BASE_URL}" \
      --token-file "${TOKEN_FILE}" \
      --mode "${SEED_MODE}" \
      --reuse-tab \
      >"${OUT}/seed-session-maestro.txt" 2>&1; then
      echo "== 预置会话：Maestro 的浏览器已注入（CDP :${MAESTRO_CDP}，mode=${SEED_MODE}）"
      SEED_MAESTRO="ok"
    else
      echo "!! 注入 Maestro 浏览器失败（见 ${OUT}/seed-session-maestro.txt）⇒ 退回 UI 登录路径" >&2
    fi
  else
    echo "!! 没找到 Maestro 的 Chrome（DevToolsActivePort）⇒ 退回 UI 登录路径" >&2
  fi
fi
printf 'seed-maestro=%s\n' "${SEED_MAESTRO}" >>"${SUMMARY}"

wait "${MAESTRO_PID}"
FLOW_RC=$?
FLOW_FAILED="$(grep -c 'FAILED' "${OUT}/19-request-logs-time-ranges.log" || true)"
tail -n 30 "${OUT}/19-request-logs-time-ranges.log"

# ── 5. 第二层：CDP 探针（语义对拍）────────────────────────────────────────
echo "== 第二层：CDP 探针（assert-request-logs-time-ranges.mjs，矩阵 ${MATRIX_ROUNDS} 轮）"
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
MATRIX_ROUNDS="${MATRIX_ROUNDS}" \
HEAVY="${HEAVY}" \
node "${ROOT}/maestro/tools/assert-request-logs-time-ranges.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  2>&1 | tee "${OUT}/assert-request-logs-time-ranges.txt"
PROBE_RC=${PIPESTATUS[0]}

printf 'flow rc=%s FAILED=%s\n' "${FLOW_RC}" "${FLOW_FAILED}" >>"${SUMMARY}"
printf 'probe rc=%s\n' "${PROBE_RC}" >>"${SUMMARY}"
echo "---- 汇总 ----"
cat "${SUMMARY}"
echo "产物目录：${OUT}"

# ── 判定：★ 三态（0=绿 / 1=红 / 2=量具失效），口径走 result.mjs ───────────
# ★ 未知退出码（2026-11-25 第 28 轮审计实测）：某一层的进程被杀（137 / 143）、被中断（130）、
#   工具崩溃、或没返回有效值 ⇒ 那一层**没有结论**。以前 case 的 *) 兜底通向 PASS
#   ⇒ **被杀掉的一轮会被判绿**。现在：mergeExitCodes 把未知退出码判为量具失效，
#   并把是哪一层的哪个码带出来写进结论行；PASS 只挂在 case 的 0) 上。
# ★ 输出是两行（未知退出码列表 / 合并后的码）：未知列表在前，
#   因为 $(...) 会吃掉结尾换行 —— 合并后的码放最后一行才不会被吃掉一半。
MERGED_OUT="$(node -e "
import('node:url').then(async (u) => {
  const m = await import(u.pathToFileURL('${ROOT}/maestro/tools/result.mjs').href)
  process.stdout.write(m.unexpectedExitCodes([${FLOW_RC}, ${PROBE_RC}]).join(',') + '\n' + String(m.mergeExitCodes([${FLOW_RC}, ${PROBE_RC}])))
})
" 2>/dev/null)"
UNEXPECTED_RC="${MERGED_OUT%%$'\n'*}"
MERGED_RC="${MERGED_OUT#*$'\n'}"
if [ -z "${MERGED_RC}" ]; then
  echo "!! 判定口径调用失败（result.mjs 的 mergeExitCodes 没返回）⇒ 量具失效"
  exit 2
fi
printf 'merged rc=%s%s\n' "${MERGED_RC}" "${UNEXPECTED_RC:+（未知退出码=${UNEXPECTED_RC}）}" >>"${SUMMARY}"
case "${MERGED_RC}" in
  1)
    echo "VERDICT: FAIL（flow rc=${FLOW_RC} FAILED=${FLOW_FAILED} / probe rc=${PROBE_RC}）"
    exit 1
    ;;
  2)
    echo "VERDICT: 量具失效（flow rc=${FLOW_RC} / probe rc=${PROBE_RC}：断言未评估，**这不是产品缺陷**${UNEXPECTED_RC:+；★ 含未知退出码 ${UNEXPECTED_RC} ⇒ 那一层是工具异常退出或被杀，先查工具}）"
    exit 2
    ;;
  0)
    echo "VERDICT: PASS（两层全绿：flow rc=${FLOW_RC} / probe rc=${PROBE_RC}）"
    exit 0
    ;;
  *)
    echo "VERDICT: 量具失效（merged rc=${MERGED_RC} 不在 0/1/2 ⇒ 判定口径返回了未知值，不当作绿）"
    exit 2
    ;;
esac
