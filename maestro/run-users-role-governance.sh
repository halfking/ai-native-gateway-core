#!/usr/bin/env bash
# maestro/run-users-role-governance.sh —— 「用户管理页角色治理」真机全链 runner
#
# ★ 三层（★ 各自独立、★ 合并后给一个唯一判定 ★）：
#   ① flow 21-users-page-role-governance.yaml（Maestro）⇒ 真实鼠标**可达性**
#   ② assert-users-role-options.mjs（正常模式）        ⇒ 控件级**行为断言**（option 集合）
#   ③ assert-users-role-options.mjs --negate           ⇒ **判据自证**（必须判红）
#
# ★★ 第 ③ 层为什么**不并进** mergeExitCodes（★ 这是本脚本最容易写错的一处 ★）：
#   `--negate` 的**期望结果就是红**（rc=1）⇒ 若把它并进合并，★ 每一轮都会判红
#   ⇒ ⇒ 所以它**单独判**：★ 「rc==1 ⇒ 自证生效（这层算通过）」、
#     「rc==0 ⇒ ★ 自证开关**静默失效** ⇒ 整轮判红」、
#     「rc==2 ⇒ 量具失效（未评估到）」。
#   ★ 判据：**自证层的红是【期望】，探针正常模式的红是【缺陷】** ⇒ ★ 两者的红方向相反
#     ⇒ 绝不能混在一个 if 链里（仓内 result.mjs 头注记过同款事故：「红被吃掉＝等于没有断言」）。
#
# ★★ 2026-11-26 改写：CDP 预置会话（根治 captcha 死锁）★★
#   原 flow 在登录段红（2 次 retry 都失败），实测服务端状态已变（16:5x 同命令绿过、
#   17:0x 必红）。后端登录失败计数纯内存无 TTL，captcha 闸门挂在任何 DB 查询与
#   bcrypt 之前 ⇒ captcha_required 时成功不可达 ⇒ 计数永不清 ⇒ UI 登录永远红。
#   ⇒ 根治 = 不走 UI 登录：在 211 本机现签 JWT（密钥不出机器）+ CDP 注入会话。
#
# 用法：
#   ADMIN_USER=admin maestro/run-users-role-governance.sh
#   BASE_URL=https://llm.itestu.cn SEED_SSH_HOST=211 maestro/run-users-role-governance.sh
#
# 环境变量：
#   BASE_URL      默认 https://llm.itestu.cn
#   SEED_ENABLE   是否走 CDP 预置会话（默认 1；=0 ⇒ 退回纯 UI 登录路径）
#   SEED_SSH_HOST 现签 JWT 的机器（默认 211；密钥**不出这台机器**）
#   SEED_MODE     注入的工作模式（默认 expert）
#   SEED_TTL      现签令牌的 TTL 秒数（默认 1800）
#   ADMIN_USER    默认从仓根 .env.local 读（预置路径不需要口令）
#   MAESTRO_BIN   默认 maestro
#   MAESTRO_DRIVER_PORT Maestro 用的 CDP 端口（默认 9222；**被占用则顺延**）
#   PROBE_CDP_PORT      探针用的 CDP 端口（默认 9224；同上）
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
PROBE_CDP_PORT="${PROBE_CDP_PORT:-9224}"
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x1400}"
MAESTRO_BIN="${MAESTRO_BIN:-maestro}"
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

command -v "$MAESTRO_BIN" >/dev/null 2>&1 || { echo "!! 找不到 maestro（$MAESTRO_BIN）⇒ 量具失效"; exit 2; }
command -v node >/dev/null 2>&1 || { echo "!! 找不到 node ⇒ 量具失效"; exit 2; }

# ── 2. 凭据 ────────────────────────────────────────────────────────────────
ENV_FILE="${ROOT}/.env.local"
ADMIN_USER="${ADMIN_USER:-}"
if [ -f "$ENV_FILE" ]; then
  [ -n "$ADMIN_USER" ] || ADMIN_USER="$(grep -E '^LLM_GATEWAY_ADMIN_USER=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d '\"'"'"' \r')"
fi
ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_USER

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
pick_free_port() {
  local base="$1" exclude="${2:-}" p
  for p in $(seq "$base" $((base + 20))); do
    [ -n "${exclude}" ] && [ "${p}" = "${exclude}" ] && continue
    if ! port_busy "$p"; then printf '%s' "$p"; return 0; fi
  done
  return 1
}

STAMP="$(date +%Y%m%d-%H%M%S)-users-role"
DRIVER_PORT="$(pick_free_port "${DRIVER_PORT}")" || { echo "!! ${DRIVER_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }

start_chrome() {
  local port="$1" profile="$2"
  mkdir -p "${profile}"
  nohup "${CHROME_BIN}" --headless=new --remote-debugging-port="${port}" \
    --user-data-dir="${profile}" --no-first-run --no-default-browser-check \
    --disable-gpu --lang=zh-CN --window-size=1440,1400 about:blank \
    >"${REPORT_ROOT}/.chrome-${port}.log" 2>&1 &
  CHROME_PIDS+=("$!")
  for _ in $(seq 1 30); do
    port_busy "${port}" && return 0
    sleep 1
  done
  echo "!! Chrome(:${port}) 起不来（看 ${REPORT_ROOT}/.chrome-${port}.log）" >&2
  return 1
}

start_chrome "${DRIVER_PORT}" "/tmp/urp-maestro-chrome-${STAMP}-${DRIVER_PORT}" || exit 2
PROBE_CDP_PORT="$(pick_free_port "${PROBE_CDP_PORT}" "${DRIVER_PORT}")" || { echo "!! ${PROBE_CDP_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }
start_chrome "${PROBE_CDP_PORT}" "/tmp/urp-probe-chrome-${STAMP}-${PROBE_CDP_PORT}" || exit 2
echo "== CDP 端口：Maestro=${DRIVER_PORT} 探针=${PROBE_CDP_PORT}"

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="${ROOT}/maestro/reports/${STAMP}-users-role-governance"
mkdir -p "$OUT"
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
SEED_MINT_SCRIPT="${ROOT}/maestro/tools/mint-jwt-token.py"

# 在 211 本机现签（**签名密钥不出这台机器**）
seed_mint() {
  local host="$1" ttl="$2" out="$3"
  [ -f "${SEED_MINT_SCRIPT}" ] || { echo "!! 找不到 ${SEED_MINT_SCRIPT}" >&2; return 1; }
  scp -q -o ConnectTimeout=10 "${SEED_MINT_SCRIPT}" "${host}:/tmp/mint-jwt-token.py" 2>/dev/null || return 1
  ssh -o ConnectTimeout=10 "${host}" "python3 /tmp/mint-jwt-token.py --ttl ${ttl} --output /tmp/llmgw-jwt-$(date +%s).txt && cat /tmp/llmgw-jwt-*.txt | tail -1" >"${out}" 2>/dev/null || return 1
  [ -s "${out}" ] || return 1
  return 0
}

SEED_STATE="off"
if [ "${SEED_ENABLE}" = "1" ]; then
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

  if [ -n "${TOKEN_FILE}" ]; then
    # 先验令牌：/api/auth/me 必须 200
    SEED_ME_RC="$(curl -s -m 15 -o /dev/null -w '%{http_code}' \
      -H "Cookie: llmgw_session=$(cat "${TOKEN_FILE}")" "${BASE_URL}/api/auth/me" 2>/dev/null || echo 000)"
    if [ "${SEED_ME_RC}" != "200" ]; then
      echo "!! 令牌未被后端认（/api/auth/me=${SEED_ME_RC}）⇒ 不注入" >&2
      rm -f "${TOKEN_FILE}"
      TOKEN_FILE=""
      TOKEN_FILE_OWNED=0
    else
      SEED_STATE="ok"
      # 注入探针的 Chrome
      if run_with_timeout 60 node "${ROOT}/maestro/tools/seed-session.mjs" \
        --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
        --url "${BASE_URL}" \
        --token-file "${TOKEN_FILE}" \
        --mode "${SEED_MODE}" \
        --reuse-tab \
        >"${OUT}/seed-session-probe.txt" 2>&1; then
        echo "== 预置会话：探针 Chrome 已注入（CDP :${PROBE_CDP_PORT}，mode=${SEED_MODE}）"
      else
        echo "!! 预置会话：探针 Chrome 注入失败（见 ${OUT}/seed-session-probe.txt）" >&2
        SEED_STATE="partial"
      fi
    fi
  fi
fi
printf 'seed-state=%s\n' "${SEED_STATE}" >>"${SUMMARY}"

# ── 4. 第一层：Maestro flow（放后台，待 Chrome 起来后注入会话）───────────────
echo "== 第一层：Maestro flow（21-users-page-role-governance.yaml）"
"$MAESTRO_BIN" test \
  --driver-host-port "${DRIVER_PORT}" \
  --headless --no-ansi \
  --screen-size "$SCREEN_SIZE" \
  --test-output-dir "${OUT}/flow" \
  -e BASE_URL="$BASE_URL" \
  -e ADMIN_USER="$ADMIN_USER" \
  -e ADMIN_PASSWORD="" \
  -e ADMIN_DISPLAY_NAME=系统管理员 \
  maestro/flows/21-users-page-role-governance.yaml >"${OUT}/flow.log" 2>&1 &
MAESTRO_PID=$!

# ── 4b. 往 Maestro 自己的 Chrome 注入预置会话 ───────────────────────────────
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
tail -6 "${OUT}/flow.log" | sed 's/^/  /'

# ★★ 红分类：★ flow 红在【登录段】⇒ 那不是产品红 ⇒ 判**量具失效**。
FLOW_VERDICT="$FLOW_RC"
FLOW_NOTE=""
if [ "$FLOW_RC" != "0" ]; then
  if grep -q "Run _shared/login.yaml.*FAILED" "${OUT}/flow.log" 2>/dev/null; then
    FLOW_VERDICT=2
    FLOW_NOTE="★ flow 红在【登录段】⇒ 判据未评估到产品行为 ⇒ 按量具失效计（非产品红）"
  elif grep -qE "Unknown Property|YamlException|ParseException|Unable to parse" "${OUT}/flow.log" 2>/dev/null; then
    FLOW_VERDICT=2
    FLOW_NOTE="★ flow 在【解析阶段】就失败（YAML/命令不合法）⇒ 判据压根没跑到页面 ⇒ 按量具失效计"
  fi
  [ -n "$FLOW_NOTE" ] && echo "  $FLOW_NOTE"
fi

# ── 层间编排隔离 ──────────────────────────────────────────────────────────
sleep "${LAYER_GAP:-3}"

# ── 5. 第二层：探针（正常模式）───────────────────────────────────────────────
echo "== 第二层：探针（正常模式：option 集合断言）"
if ! curl -sS --max-time 5 "http://127.0.0.1:${PROBE_CDP_PORT}/json/version" >/dev/null 2>&1; then
  echo "  !! CDP ${PROBE_CDP_PORT} 探不活 ⇒ 量具失效"
  PROBE_RC=2
  NEGCTL_RC=2
  : >"${OUT}/probe.log"
  : >"${OUT}/probe-negate.log"
else
  node maestro/tools/assert-users-role-options.mjs \
    --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" --url "$BASE_URL" >"${OUT}/probe.log" 2>&1
  PROBE_RC=$?
  tail -4 "${OUT}/probe.log" | sed 's/^/  /'
  sleep "${LAYER_GAP:-3}"

  # ── 6. 第三层：探针（--negate）────────────────────────────────────────────
  echo "== 第三层：探针（--negate：★ 期望判红）"
  node maestro/tools/assert-users-role-options.mjs \
    --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" --url "$BASE_URL" --negate >"${OUT}/probe-negate.log" 2>&1
  NEGCTL_RC=$?
  tail -3 "${OUT}/probe-negate.log" | sed 's/^/  /'
fi

# ── 判定 ────────────────────────────────────────────────────────────────
SELF_CLAIM_OK=0
if [ "$NEGCTL_RC" = "1" ]; then
  SELF_CLAIM_OK=1
  SELF_LINE="自证层：--negate 如期判红（rc=1）⇒ ★ 判据可证伪"
elif [ "$NEGCTL_RC" = "0" ]; then
  SELF_LINE="自证层：★ --negate 判绿（rc=0）⇒ ★ 自证开关静默失效 ⇒ 本轮结论不可信"
else
  SELF_LINE="自证层：--negate 量具失效（rc=${NEGCTL_RC}）⇒ 未能评估判据是否可证伪"
fi

MERGED_OUT="$(node -e "
import('node:url').then(async (u) => {
  const m = await import(u.pathToFileURL('${ROOT}/maestro/tools/result.mjs').href)
  const codes = [${FLOW_VERDICT}, ${PROBE_RC}]
  process.stdout.write(m.unexpectedExitCodes(codes).join(',') + '\n' + String(m.mergeExitCodes(codes)))
})
" 2>/dev/null)"
UNEXPECTED_RC="${MERGED_OUT%%$'\n'*}"
MERGED_RC="${MERGED_OUT#*$'\n'}"
if [ -z "${MERGED_RC}" ]; then
  echo "!! 判定口径调用失败（result.mjs 的 mergeExitCodes 没返回）⇒ 量具失效"
  exit 2
fi

{
  printf 'flow rc=%s（判定=%s）\n' "$FLOW_RC" "$FLOW_VERDICT"
  printf 'probe rc=%s\n' "$PROBE_RC"
  printf 'negate rc=%s\n' "$NEGCTL_RC"
  printf 'merged rc=%s\n' "$MERGED_RC"
  [ -n "$FLOW_NOTE" ] && printf 'flow note: %s\n' "$FLOW_NOTE"
} >>"$SUMMARY"

echo "---- 汇总 ----"
cat "$SUMMARY"
echo "$SELF_LINE"

case "$MERGED_RC" in
  0)
    if [ "$SELF_CLAIM_OK" = "1" ]; then
      echo "VERDICT: PASS（flow rc=${FLOW_RC} / probe rc=${PROBE_RC}；${SELF_LINE}）"
      exit 0
    fi
    echo "VERDICT: FAIL（${SELF_LINE}）"
    exit 1
    ;;
  1)
    echo "VERDICT: FAIL（flow rc=${FLOW_RC} / probe rc=${PROBE_RC}${UNEXPECTED_RC:+；未知退出码=${UNEXPECTED_RC}}）"
    exit 1
    ;;
  2)
    echo "VERDICT: 量具失效（flow rc=${FLOW_RC} / probe rc=${PROBE_RC}：断言未评估，**这不是产品缺陷**）"
    exit 2
    ;;
  *)
    echo "VERDICT: 量具失效（merged rc=${MERGED_RC} 不在 0/1/2 ⇒ 不当作绿）"
    exit 2
    ;;
esac
