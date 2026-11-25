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
#     ★ **口令已不是硬必需**：默认走「CDP 预置会话」（见下），不需要口令；
#       只有「预置失败且必须退回 UI 登录」时才需要它。
#   SEED_ENABLE           是否走 CDP 预置会话（默认 1；=0 ⇒ 退回纯 UI 登录路径）
#   SEED_SSH_HOST         现签 JWT 的机器（默认 211；密钥**不出这台机器**）
#   SEED_TOKEN_FILE       已备好的 JWT 文件（给了就不现签；**调用方自己负责删**）
#   SEED_MODE             注入的工作模式（默认 expert —— 本 flow 断的是专家面）
#   SEED_TTL              现签令牌的 TTL 秒数（默认 1800）
#   MAESTRO_DRIVER_PORT   Maestro 用的 CDP 端口（默认 9222；**被占用则顺延**）
#   PROBE_CDP_PORT        第二层探针用的 CDP 端口（默认 9223；同上）
#   MAESTRO_SCREEN_SIZE   视口（默认 1440x3600）
#   FLOW                  指定 flow（默认 maestro/flows/18-stats-range-and-scope-hint.yaml）
#
# ★★ 为什么默认走「预置会话」（2026-11-25 captcha 死锁根治轮）：
#   后端的登录失败计数是**纯内存、无 TTL**，只靠「成功登录一次」清零
#   （admin/auth_captcha.go:509 clearLoginFailures）；而 captcha 闸门挂在
#   **任何 DB 查询与 bcrypt 之前**（admin/auth.go:229）
#   ⇒ captcha_required 时**走不到密码校验** ⇒ 成功不可达 ⇒ 计数永不清
#   ⇒ 连续失败到阈值（CaptchaFailureThreshold=3）后，**任何走 UI 登录的自动化
#     都会永久变红**（验证码要人看图作答）。
#   ⇒ 根治 = **不走 UI 登录**：用容器里的 LLM_GATEWAY_SECRET_KEY 在 211 本机现签
#     一枚短期 JWT，再用 CDP 写进浏览器的 HttpOnly cookie
#     （maestro/tools/seed-session.mjs）⇒ 全程不碰 /api/auth/token ⇒ 不受闸门约束。
#   ★ 签名密钥**不出 211**；本地只拿到一枚会话令牌，落在 0600 临时文件里，
#     **跑完即删**（trap 负责）。
#   ★ 两个 Chrome（Maestro driver / CDP 探针）**各注入一次**
#     ——它们是两个独立 profile，cookie 不共享。
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
ADMIN_USER="${ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-admin}}"
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  ADMIN_PASSWORD="${LLM_GATEWAY_ADMIN_PASSWORD:-}"
fi
# ★ 口令**不再是硬必需**（2026-11-25）：默认走 CDP 预置会话 ⇒ 不需要口令；
#   只有「预置失败、必须退回 UI 登录」时才需要它 ⇒ 缺口令时在预置步骤后明确报错，
#   而不是在脚本开头就 :? 把人挡在门外（那等于「没口令就跑不了」的老问题）。
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
# ★ 预置会话的临时令牌文件（0600）。TOKEN_FILE_OWNED=1 ⇒ 是本脚本现签的
#   ⇒ **跑完必须删**（签名密钥虽然没出机器，但令牌等价于一把万能钥匙）。
#   调用方自己给的（SEED_TOKEN_FILE）⇒ 所有权不归本脚本 ⇒ 不删。
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
    [ -n "${exclude}" ] && [ "${p}" = "${exclude}" ] && continue
    if ! port_busy "$p"; then printf '%s' "$p"; return 0; fi
  done
  return 1
}

STAMP="$(date +%Y%m%d-%H%M%S)-stats-range"
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

# ── 3.5 预置会话（CDP seed-session）★ captcha 死锁的根治手段 ─────────────
# ★ 这一步在**跑 flow 之前**做，两个 Chrome 都要注入（它们是两个独立 profile，
#   cookie 不共享）：Maestro driver（第一层要用的浏览器）+ CDP 探针（第二层）。
#
# ★ 注入顺序有讲究：**先验令牌、再注入** —— 令牌不被后端认的话，注入只是把
#   一张废卡塞进浏览器，后面照样走成访客（而访客 ⇒ 弹窗出现 ⇒ UI 登录 ⇒
#   captcha 死锁）⇒ 所以先用一次 /api/auth/me 把「令牌有效」这件事钉住。
# ★ 带超时的执行（macOS 的 bash 3.2 **没有 `timeout` 命令** ⇒ 自己实现）。
#   用法：run_with_timeout <秒> <命令...>；超时 ⇒ 杀掉该进程并返回非 0。
# ★ 为什么需要：工具进程挂住 ⇒ 整个 runner 跟着挂住（2026-11-25 实测：
#   seed-session 的 WebSocket close 握手不返回 ⇒ 进程不退出 ⇒ runner 卡死）。
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

# 在 211 本机现签（**签名密钥不出这台机器**）；令牌只落 0600 临时文件，不打印。
seed_mint() {
  local host="$1" ttl="$2" out="$3" py
  [ -f "${SEED_HELPER}" ] || { echo "!! 找不到 ${SEED_HELPER}" >&2; return 1; }
  scp -q -o ConnectTimeout=10 "${SEED_HELPER}" "${host}:/tmp/api-211-admin-probe.py" 2>/dev/null || return 1
  # ★ 脚本体走 stdin 传给远端 python3 -（避免多层引号转义把代码搞坏）；
  #   远端 stdout 只有令牌本身 ⇒ 直接重定向进 0600 文件。
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
    # 先验令牌：/api/auth/me 必须 200（**只取状态码，不打响应体** ——
    # 响应体里带着完整 access_token，落进日志就等于把它抄了一份出去）。
    SEED_ME_RC="$(curl -s -m 15 -o /dev/null -w '%{http_code}' \
      -H "Cookie: llmgw_session=$(cat "${TOKEN_FILE}")" "${BASE_URL}/api/auth/me" 2>/dev/null || echo 000)"
    if [ "${SEED_ME_RC}" != "200" ]; then
      echo "!! 令牌未被后端认（/api/auth/me=${SEED_ME_RC}）⇒ 不注入" >&2
      rm -f "${TOKEN_FILE}"
      TOKEN_FILE=""
      TOKEN_FILE_OWNED=0
    else
      SEED_STATE="ok"
      for port in "${DRIVER_PORT}" "${PROBE_CDP_PORT}"; do
        if run_with_timeout 60 node "${ROOT}/maestro/tools/seed-session.mjs" \
          --cdp "http://127.0.0.1:${port}" \
          --url "${BASE_URL}" \
          --token-file "${TOKEN_FILE}" \
          --mode "${SEED_MODE}" \
          >"${OUT}/seed-session-${port}.txt" 2>&1; then
          echo "== 预置会话：CDP :${port} 已注入（mode=${SEED_MODE}）"
        else
          echo "!! 预置会话：CDP :${port} 注入失败（见 ${OUT}/seed-session-${port}.txt）" >&2
          SEED_STATE="partial"
        fi
      done
    fi
  fi
fi

if [ "${SEED_STATE}" != "ok" ] && [ -z "${ADMIN_PASSWORD}" ]; then
  echo "!! 既没预置成功、又没管理员口令 ⇒ 本轮无法建立会话（量具失效，不是产品缺陷）" >&2
  echo "!! 预置失败原因见上面日志；口令可用 ADMIN_PASSWORD=<...> 提供。" >&2
  exit 2
fi
printf 'seed-state=%s\n' "${SEED_STATE}" >>"${SUMMARY}"

# ── 4. 第一层：Maestro flow ───────────────────────────────────────────────
# ★★ 为什么这一步要**放后台**：Maestro 的 **web 驱动自起 Chrome**
#   （Selenium 的 ChromeDriver + 临时 profile `org.chromium.Chromium.scoped_dir.*`），
#   ⇒ **`--driver-host-port` 对 web 完全无效**（那是给 Android adb forward 用的）
#   ⇒ runner 自己起的那个 Chrome **根本不是 flow 跑的那个浏览器**
#   ⇒ 想让 flow 跑在预置会话上，就必须往 **Maestro 自己的**浏览器里注入
#   ⇒ 而它只在启动后才存在 ⇒ ⇒ maestro 放后台，注入完成后再等它结束。
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
  "${FLOW}" >"${OUT}/18-stats-range-and-scope-hint.log" 2>&1 &
MAESTRO_PID=$!

# ── 4b. 往 **Maestro 自己的** Chrome 注入预置会话 ───────────────────────────
# ★ 怎么找到它：chromedriver 是 maestro 进程的子进程、Chrome 是 chromedriver 的
#   子进程 ⇒ 顺着**父子关系**找（不靠「扫 scoped_dir 目录」——那会误伤别的会话
#   正在跑的 Maestro）⇒ 从 Chrome 的命令行里取 --user-data-dir
#   ⇒ ChromeDriver 会把 DevTools 端口写进 <profile>/DevToolsActivePort
#   ⇒ 那个端口就是 CDP 入口。
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
FLOW_FAILED="$(grep -c 'FAILED' "${OUT}/18-stats-range-and-scope-hint.log" || true)"
tail -n 25 "${OUT}/18-stats-range-and-scope-hint.log"

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

# ── 5b. 第二层·补充：自定义区间的网络层证据（独立工具）────────────────────
# ★ 为什么独立成一步（而不是塞进主探针）：
#   这段要**驱动日历组件的内部状态**（Element Plus 的 ElDatePicker：填值 →
#   补发 change → 点应用），步骤多、失败模式多 ⇒ 独立成工具 ⇒
#   它的量具缺陷**不会把主探针的其它 26 条一起拖红**，也不会污染主探针的绿。
# ★ 验的是主探针**没覆盖**的那条路径：主探针验预设档位（days=7），
#   这里验**自定义区间 → start/end 透传**（本轮改动最大的那条路径）。
echo "== 第二层·补充：自定义区间（assert-board-op-custom-range.mjs）"
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
node "${ROOT}/maestro/tools/assert-board-op-custom-range.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  2>&1 | tee "${OUT}/assert-board-op-custom-range.txt"
CUSTOM_RC=${PIPESTATUS[0]}

# ── 5c. 第二层·补充：列表页**深链**对拍（独立工具）────────────────────────
# ★ 为什么独立成这一步（第 30 轮）：
#   列表页（/admin/board-operational/:dimension）并入共享日期组件后，多了一条
#   **别的页都没有**的能力：URL 深链（?start=&end=）直接决定初始区间 ⇒
#   ⇒ 这条能力在生产上**没有独立证据**（不是缺信心，是缺覆盖）⇒ 这里补上。
# ★ 判据全是**网络层 / DOM 层**证据：深链真的到达后端（请求带 start/end）、
#   显示的区间 == 请求窗口、深链优先于 days、无参时默认 7 天。
echo "== 第二层·补充：列表页深链（assert-board-op-list-deeplink.mjs）"
ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
node "${ROOT}/maestro/tools/assert-board-op-list-deeplink.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --user "${ADMIN_USER}" \
  2>&1 | tee "${OUT}/assert-board-op-list-deeplink.txt"
DEEPLINK_RC=${PIPESTATUS[0]}

printf 'flow rc=%s FAILED=%s\n' "${FLOW_RC}" "${FLOW_FAILED}" >>"${SUMMARY}"
printf 'probe rc=%s\n' "${PROBE_RC}" >>"${SUMMARY}"
printf 'custom-range rc=%s\n' "${CUSTOM_RC}" >>"${SUMMARY}"
printf 'list-deeplink rc=%s\n' "${DEEPLINK_RC}" >>"${SUMMARY}"
echo "---- 汇总 ----"
cat "${SUMMARY}"
echo "产物目录：${OUT}"

# ── 判定：★ 三态（0=绿 / 1=红 / 2=量具失效）──────────────────────────────
# ★★ 合并口径**不在这个 shell 里写** ⇒ 调 maestro/tools/result.mjs 的
#   mergeExitCodes（仓内判定口径的**唯一真源**）：
#     跨工具优先级 = **红 > 量具失效 > 绿**
#     （与「同一个工具内 gauge 优先」是两条**故意不同**的口径，理由见 result.mjs 头注②）
# ★★ 为什么必须调它（2026-11-25 审计轮实测撞出）：
#   本文件此前自己写 if 链 ⇒ 那是**第三份判定口径** ⇒ 必然漂移；
#   而且那个顺序让「探针量具失效」**吃掉了补充层的红** ⇒
#   **红被吃掉＝等于没有断言**（与 result.mjs 头注记录的历史事故同类）。
# ★ 未知退出码（2026-11-25 第 28 轮审计实测）：某一层的进程被杀（137 / 143）、被中断（130）、
#   工具崩溃、或没返回有效值 ⇒ 那一层**没有结论**。以前 case 的 *) 兜底通向 PASS
#   ⇒ **被杀掉的一轮会被判绿**。现在：mergeExitCodes 把未知退出码判为量具失效，
#   并把是哪一层的哪个码带出来写进结论行；PASS 只挂在 case 的 0) 上。
# ★ 输出是两行（未知退出码列表 / 合并后的码）：未知列表在前，
#   因为 $(...) 会吃掉结尾换行 —— 合并后的码放最后一行才不会被吃掉一半。
MERGED_OUT="$(node -e "
import('node:url').then(async (u) => {
  const m = await import(u.pathToFileURL('${ROOT}/maestro/tools/result.mjs').href)
  process.stdout.write(m.unexpectedExitCodes([${FLOW_RC}, ${PROBE_RC}, ${CUSTOM_RC}, ${DEEPLINK_RC}]).join(',') + '\n' + String(m.mergeExitCodes([${FLOW_RC}, ${PROBE_RC}, ${CUSTOM_RC}, ${DEEPLINK_RC}])))
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
    echo "VERDICT: FAIL（flow rc=${FLOW_RC} FAILED=${FLOW_FAILED} / probe rc=${PROBE_RC} / custom-range rc=${CUSTOM_RC} / list-deeplink rc=${DEEPLINK_RC}）"
    exit 1
    ;;
  2)
    echo "VERDICT: 量具失效（flow rc=${FLOW_RC} / probe rc=${PROBE_RC} / custom-range rc=${CUSTOM_RC} / list-deeplink rc=${DEEPLINK_RC}：断言未评估，**这不是产品缺陷**${UNEXPECTED_RC:+；★ 含未知退出码 ${UNEXPECTED_RC} ⇒ 那一层是工具异常退出或被杀，先查工具}）"
    exit 2
    ;;
  0)
    echo "VERDICT: PASS（两层全绿：flow rc=${FLOW_RC} / probe rc=${PROBE_RC} / custom-range rc=${CUSTOM_RC} / list-deeplink rc=${DEEPLINK_RC}）"
    exit 0
    ;;
  *)
    echo "VERDICT: 量具失效（merged rc=${MERGED_RC} 不在 0/1/2 ⇒ 判定口径返回了未知值，不当作绿）"
    exit 2
    ;;
esac
