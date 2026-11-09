#!/usr/bin/env bash
# maestro/run-mock-only.sh — 把既有 01..09 Maestro flow 指向 mock-only 网关。
#
# 与 run-local.sh 的唯一差别：凭据与 BASE_URL 不读仓根 .env.local（那是
# 本地开发网关的口令），而是用 mock-only 栈自己的凭据（可被环境变量覆盖）。
# 其余（JDK 探测 / CDP Chrome / resolve-ids / 逐 flow 实跑 / 慢页量具）全部复用。
#
# 前置：tests/mock-system-test/run-mock-only.sh up 已经把 mock-only 网关拉起来。
# 用法：
#   maestro/run-mock-only.sh                      # 跑 01..09
#   maestro/run-mock-only.sh --list
#   BASE_URL=http://192.168.31.211:18781 maestro/run-mock-only.sh   # 打 211
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

BASE_URL="${BASE_URL:-http://127.0.0.1:${MOCK_GATEWAY_PORT:-18781}}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9223}"   # 独立 CDP 口，不与 run-local.sh 的 9222 抢
REPORT_ROOT="${ROOT}/maestro/reports"
ADMIN_USER="${MOCK_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${MOCK_ADMIN_PASSWORD:-mock-only-admin-password}"

WITH_DEV=0
FLOWS=()
LIST_ONLY=0
for arg in "$@"; do
  case "${arg}" in
    --with-dev) WITH_DEV=1 ;;
    --list) LIST_ONLY=1 ;;
    *) FLOWS+=("${arg}") ;;
  esac
done

if [ "${#FLOWS[@]}" -eq 0 ]; then
  FLOWS=(
    maestro/flows/01-public-guest.yaml
    maestro/flows/02-overview-live.yaml
    maestro/flows/03-providers-models.yaml
    maestro/flows/04-tenants-portal.yaml
    maestro/flows/05-routing.yaml
    maestro/flows/06-keys-requests.yaml
    maestro/flows/07-admin-config.yaml
    maestro/flows/08-admin-data-ops.yaml
    maestro/flows/09-ops-maintain.yaml
  )
  if [ "${WITH_DEV}" -eq 1 ]; then
    FLOWS+=( maestro/flows/90-dev-only.yaml )
  fi
fi

if [ "${LIST_ONLY}" = "1" ]; then
  printf '%s\n' "${FLOWS[@]}"
  exit 0
fi

# ── JDK（与 run-local.sh 同源）───────────────────────────────────────────
find_jdk() {
  local c
  for c in "${MAESTRO_JAVA_HOME:-}" "${JAVA_HOME:-}"; do
    [ -n "${c}" ] && [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  done
  if command -v /usr/libexec/java_home >/dev/null 2>&1; then
    c="$(/usr/libexec/java_home -v 17 2>/dev/null || true)"
    [ -n "${c}" ] && [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  fi
  for c in /opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
           "${HOME}"/kaixuan/jdk17/Contents/Home \
           "${HOME}"/jdk*/Contents/Home \
           /Library/Java/JavaVirtualMachines/*/Contents/Home; do
    [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  done
  printf ''
}
JAVA_HOME_CANDIDATE="$(find_jdk)"
if [ -z "${JAVA_HOME_CANDIDATE}" ]; then
  echo "!! 找不到 JDK 17。请设 MAESTRO_JAVA_HOME=<jdk 根>。" >&2
  exit 2
fi
export JAVA_HOME="${JAVA_HOME_CANDIDATE}"
export PATH="${JAVA_HOME}/bin:${PATH}"

if ! command -v maestro >/dev/null 2>&1; then
  echo "!! PATH 里没有 maestro。" >&2
  exit 2
fi

# ── 前置：mock-only 网关必须活着 ────────────────────────────────────────
if ! curl -sf --max-time 3 "${BASE_URL}/healthz" >/dev/null 2>&1; then
  echo "!! mock-only 网关不可达：${BASE_URL}（先跑 tests/mock-system-test/run-mock-only.sh up）" >&2
  exit 2
fi

# ── Chrome（CDP）─────────────────────────────────────────────────────────
find_chrome() {
  if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
  if command -v chromium >/dev/null 2>&1; then command -v chromium; return; fi
  if command -v google-chrome >/dev/null 2>&1; then command -v google-chrome; return; fi
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
    [ -x "${c}" ] && { printf '%s' "${c}"; return; }
  done
  printf ''
}
CHROME_BIN="$(find_chrome)"
if [ -z "${CHROME_BIN}" ]; then
  echo "!! 找不到 Chrome。用 MAESTRO_CHROME_BIN 指定。" >&2
  exit 2
fi
CHROME_PROFILE="${MAESTRO_CHROME_PROFILE:-/tmp/maestro-mock-only-chrome-profile}"
if ! curl -s --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
  echo "== 自起 Chrome（CDP :${DRIVER_PORT}）"
  mkdir -p "${CHROME_PROFILE}"
  nohup "${CHROME_BIN}" \
    --headless=new --remote-debugging-port="${DRIVER_PORT}" \
    --user-data-dir="${CHROME_PROFILE}" \
    --no-first-run --no-default-browser-check --disable-gpu \
    about:blank >"${REPORT_ROOT}-mock-only.chrome.log" 2>&1 &
  for _ in $(seq 1 30); do
    curl -s --max-time 2 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1 && break
    sleep 1
  done
  if ! curl -s --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
    echo "!! Chrome 起不来（看 ${REPORT_ROOT}-mock-only.chrome.log）" >&2
    exit 2
  fi
fi

# ── 动态路由真实 ID（从 mock-only 网关解析；压测后库里有真实数据）─────────
ID_ARGS=()
if node "${ROOT}/maestro/tools/resolve-ids.mjs" >/tmp/maestro-mock-only-ids.txt 2>/tmp/maestro-mock-only-ids.err; then
  while IFS= read -r line; do
    case "${line}" in
      [A-Z_]*=*) ID_ARGS+=( -e "${line}" ) ;;
    esac
  done </tmp/maestro-mock-only-ids.txt
  echo "== 动态路由 ID：$(wc -l </tmp/maestro-mock-only-ids.txt | tr -d ' ') 个"
else
  echo "   解析动态路由 ID 失败（flows 会退化为空 URL 断言页骨架）" >&2
fi

STAMP="$(date +%Y%m%d-%H%M%S)-mock-only"
OUT="${REPORT_ROOT}/${STAMP}"
mkdir -p "${OUT}"
SUMMARY="${OUT}/summary.txt"
: >"${SUMMARY}"

overall=0
for flow in "${FLOWS[@]}"; do
  name="$(basename "${flow}" .yaml)"
  echo "==================== ${name} (mock-only) ===================="
  set +e
  maestro test \
    --driver-host-port "${DRIVER_PORT}" \
    --headless \
    --no-ansi \
    --test-output-dir "${OUT}/${name}" \
    -e "BASE_URL=${BASE_URL}" \
    -e "ADMIN_USER=${ADMIN_USER}" \
    -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
    "${ID_ARGS[@]+"${ID_ARGS[@]}"}" \
    "${flow}" 2>&1 | tee "${OUT}/${name}.log"
  rc=${PIPESTATUS[0]}
  set -e
  failed="$(grep -c 'FAILED' "${OUT}/${name}.log" || true)"
  warned="$(grep -c 'WARNED' "${OUT}/${name}.log" || true)"
  printf '%-28s rc=%-3s FAILED=%-4s WARNED=%s\n' "${name}" "${rc}" "${failed}" "${warned}" >>"${SUMMARY}"
  echo "--- ${name}: rc=${rc} FAILED=${failed} WARNED=${warned}"
  [ "${rc}" -ne 0 ] && overall=1
done

echo
echo "==== 汇总（mock-only）===="
cat "${SUMMARY}"
echo "产物目录：${OUT}"
exit "${overall}"
