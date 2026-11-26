#!/usr/bin/env bash
# maestro/run-usage-trend-totals.sh —— 「用量趋势汇总表合计行」的**实跑入口**
#   （单层 CDP 探针：DOM 结构判据 + 独立期望值逐字比对）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 与 run-stats-range-picker.sh 同源的结构（Chrome/seed/判定口径），       │
# │ ★ 但只跑第二层（CDP 探针），不起 Maestro flow：                          │
# │   本轮改的是「表里多一行合计」——数据正确性判据必须**读值比对**，          │
# │   Maestro 的 DSL（assertVisible）只能量「页面上有没有某个字」，           │
# │   量不了「那一格的数字与 API 重算的期望是否逐字相等」（L135 判据口径）。   │
# │   ⇒ 结构层证据（合计行真的渲染出来了）由探针的 footer 判据承载；          │
# │   ⇒ 数值层证据（四指标总额逐字正确）由探针的独立期望值比对承载。          │
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   BASE_URL=https://llm.itestu.cn \
#   bash maestro/run-usage-trend-totals.sh
#
# 环境变量：
#   BASE_URL        管理后台地址（默认 https://llm.itestu.cn）
#   QSTART/QEND/QMETRIC  深链参数（默认 2026-11-24 / 2026-11-24 / requests，
#                   复刻 owner 诉求 URL 的形态）
#   SEED_SSH_HOST   现签 JWT 的机器（默认 211；密钥**不出这台机器**）
#   PROBE_CDP_PORT  探针 Chrome 的 CDP 端口（默认 9223；被占用则顺延）
#
# ★ 走「CDP 预置会话」不碰 UI 登录（2026-11-25 captcha 死锁根治轮的范式）：
#   211 本机现签短期 JWT（签名密钥不出机器）→ /api/auth/me 验令牌 →
#   seed-session.mjs 注入探针 Chrome 的 HttpOnly cookie。令牌 0600 落盘、跑完即删。
#
# ★ 退出码（result.mjs 口径）：0=全绿；1=有红（产品问题）；2=量具失效。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
PROBE_CDP_PORT="${PROBE_CDP_PORT:-9223}"
SEED_SSH_HOST="${SEED_SSH_HOST:-211}"
SEED_MODE="${SEED_MODE:-expert}"
SEED_TTL="${SEED_TTL:-1800}"
QSTART="${QSTART:-2026-11-24}"
QEND="${QEND:-2026-11-24}"
QMETRIC="${QMETRIC:-requests}"
REPORT_ROOT="${ROOT}/maestro/reports"
mkdir -p "${REPORT_ROOT}"

find_chrome() {
  if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
  command -v chromium >/dev/null 2>&1 && { command -v chromium; return; }
  command -v google-chrome >/dev/null 2>&1 && { command -v google-chrome; return; }
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
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
  local base="$1" p
  for p in $(seq "$base" $((base + 20))); do
    if ! port_busy "$p"; then printf '%s' "$p"; return 0; fi
  done
  return 1
}

STAMP="$(date +%Y%m%d-%H%M%S)-usage-totals"
PROBE_CDP_PORT="$(pick_free_port "${PROBE_CDP_PORT}")" || { echo "!! ${PROBE_CDP_PORT} 起的 20 个端口都被占用。" >&2; exit 2; }

PROFILE="/tmp/ute-totals-chrome-${STAMP}-${PROBE_CDP_PORT}"
mkdir -p "${PROFILE}"
nohup "${CHROME_BIN}" --headless=new --remote-debugging-port="${PROBE_CDP_PORT}" \
  --user-data-dir="${PROFILE}" --no-first-run --no-default-browser-check \
  --disable-gpu --lang=zh-CN --window-size=1440,3600 about:blank \
  >"${REPORT_ROOT}/.chrome-${PROBE_CDP_PORT}.log" 2>&1 &
CHROME_PIDS+=("$!")
for _ in $(seq 1 30); do
  port_busy "${PROBE_CDP_PORT}" && break
  sleep 1
done
port_busy "${PROBE_CDP_PORT}" || { echo "!! Chrome(:${PROBE_CDP_PORT}) 起不来。" >&2; exit 2; }
echo "== 探针 Chrome CDP :${PROBE_CDP_PORT}（全新 profile，干净起点）"

OUT="${REPORT_ROOT}/${STAMP}"
mkdir -p "${OUT}"

# ── 预置会话：211 本机现签 JWT → 验令牌 → CDP 注入 ─────────────────────────
SEED_HELPER="${ROOT}/scripts/audit/api-211-admin-probe.py"
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

TOKEN_FILE="$(mktemp "${TMPDIR:-/tmp}/llmgw-jwt.XXXXXX")"
chmod 600 "${TOKEN_FILE}"
TOKEN_FILE_OWNED=1
echo "== 预置会话：在 ${SEED_SSH_HOST} 本机现签 JWT（ttl=${SEED_TTL}s，密钥不出机器）"
scp -q -o ConnectTimeout=10 "${SEED_HELPER}" "${SEED_SSH_HOST}:/tmp/api-211-admin-probe.py" 2>/dev/null || {
  echo "!! 探针脚本上 211 失败" >&2; exit 2
}
PY="import importlib.util,sys
spec=importlib.util.spec_from_file_location('p','/tmp/api-211-admin-probe.py')
m=importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
sys.stdout.write(m.mint_token(ttl=${SEED_TTL}))"
printf '%s' "${PY}" | ssh -o ConnectTimeout=10 "${SEED_SSH_HOST}" 'python3 -' >"${TOKEN_FILE}" 2>/dev/null || {
  echo "!! 现签失败" >&2; exit 2
}
[ -s "${TOKEN_FILE}" ] || { echo "!! 现签产出为空" >&2; exit 2; }
echo "== 预置会话：现签完成（令牌落 0600 临时文件，跑完自动删）"

SEED_ME_RC="$(curl -s -m 15 -o /dev/null -w '%{http_code}' \
  -H "Cookie: llmgw_session=$(cat "${TOKEN_FILE}")" "${BASE_URL}/api/auth/me" 2>/dev/null || echo 000)"
if [ "${SEED_ME_RC}" != "200" ]; then
  echo "!! 令牌未被后端认（/api/auth/me=${SEED_ME_RC}）⇒ 本轮量具失效" >&2
  exit 2
fi
echo "== 令牌验证 200 ⇒ 注入探针 Chrome"
if run_with_timeout 60 node "${ROOT}/maestro/tools/seed-session.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --token-file "${TOKEN_FILE}" \
  --mode "${SEED_MODE}" \
  >"${OUT}/seed-session.txt" 2>&1; then
  echo "== 预置会话：CDP :${PROBE_CDP_PORT} 已注入（mode=${SEED_MODE}）"
else
  echo "!! 注入失败（见 ${OUT}/seed-session.txt）" >&2
  exit 2
fi
rm -f "${TOKEN_FILE}"
TOKEN_FILE=""
TOKEN_FILE_OWNED=0

# ── 第二层：CDP 探针（结构 + 数值）─────────────────────────────────────────
echo "== 探针：合计行结构 + 四指标总额与独立期望逐字比对"
node "${ROOT}/maestro/tools/assert-usage-trend-totals.mjs" \
  --cdp "http://127.0.0.1:${PROBE_CDP_PORT}" \
  --url "${BASE_URL}" \
  --start "${QSTART}" --end "${QEND}" --metric "${QMETRIC}" \
  2>&1 | tee "${OUT}/assert-totals.txt"
RC="${PIPESTATUS[0]}"
echo "== 探针 rc=${RC}（0=绿 1=产品红 2=量具失效）；证据目录 ${OUT}"
exit "${RC}"
