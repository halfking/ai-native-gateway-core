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
# 视口：web 驱动默认只有 1024×625，折叠线以下的内容不可见 ⇒ 放大到桌面尺寸
# （与 maestro/run-simple-overview.sh、maestro/run-stage-tree.sh 同一惯例）。
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x1400}"
ADMIN_USER="${MOCK_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${MOCK_ADMIN_PASSWORD:-mock-only-admin-password}"
# ★ login.yaml 判据②③的顶栏 display_name（默认值放调用侧）
ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-系统管理员}"

# ★ 必须 export：子进程 `resolve-ids.mjs` 要靠这两个环境变量问**本栈**要动态 ID。
#   不 export ⇒ 子进程拿不到 BASE_URL ⇒ 回落成 resolve-ids 自己的默认值
#   http://127.0.0.1:8782（本地开发网关）⇒ 把**别的网关**的 provider/key/tenant id
#   灌给本栈的 flow ⇒ 三个详情页（供应商/租户/密钥）全部打不开
#   （实测：invalid provider id / tenant not found / invalid id —— 全是「id 属于别的栈」）。
#   凭据同理：不 export ⇒ resolve-ids 回落读仓根 .env.local（本地开发口令）⇒ 登录失败。
export BASE_URL
export LLM_GATEWAY_ADMIN_USER="${ADMIN_USER}"
export LLM_GATEWAY_ADMIN_PASSWORD="${ADMIN_PASSWORD}"
REPORT_ROOT="${ROOT}/maestro/reports"

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

# ── 前置：把栈状态归一化（gate 会跑 normalize_after_boot）──────────────
#   为什么必须先跑：mock 栈的 seed 管理员口令是一次性的，网关播种时写了
#   must_change_password=TRUE ⇒ 浏览器登录后被「修改密码」弹窗接管，
#   且其余管理 API 被 errPasswordChangeRequired 全拒 ⇒ 供应商/租户/密钥
#   三个详情页的断言必红（与本仓代码无关，是栈状态问题）。
if [ -x "${ROOT}/tests/mock-system-test/run-mock-only.sh" ] || [ -f "${ROOT}/tests/mock-system-test/run-mock-only.sh" ]; then
  echo "== 归一化 mock-only 栈状态（run-mock-only.sh gate）"
  bash "${ROOT}/tests/mock-system-test/run-mock-only.sh" gate 2>&1 | sed 's/^/   /' || \
    echo "   （栈门未全绿，仍继续跑 flows —— 失败归因见下面的逐 flow 结果）"
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
# ★ 2026-11-17（245 部署后验收轮）：**profile 默认每次运行都是新的**。
#
#   改动理由（**真因不是它**，见下）：Chrome 的 profile 目录被跨运行复用时，
#   HTTP 缓存里会留着**上一次部署的旧 bundle** ⇒ 前端改版后，flow 可能读到
#   **被测对象的旧版本** ⇒ 症状具有欺骗性（看起来像「部署后前端坏了」）。
#   ⇒ 该隐患**本身成立**，故默认 profile 改为**带时间戳的全新目录**；
#     需要复现旧行为时用 MAESTRO_CHROME_PROFILE 显式指定固定目录。
#
#   ⚠ ★ 但本轮 245 上 flow 01/02/09 的失败**与缓存无关**（已用全新 profile
#     复跑，失败集合逐项相同）⇒ 真因是 **245 的公网入口把 `/` 与运维域页面
#     交给了 Maintain 平台**（`/` → 302 → `/maintain/home`；`/ops/licenses`
#     渲染的是「AI Native 运维中心」的 License 管理页）⇒ 而仓内 flow 的断言
#     面向的是**网关自带 UI** ⇒ ⇒ 那三个 flow 在 245 上**不适用**（量具边界），
#     不是产品回归 ⇒ 见审计主档 §8.13。
CHROME_PROFILE="${MAESTRO_CHROME_PROFILE:-/tmp/maestro-mock-only-chrome-profile-$(date +%Y%m%d-%H%M%S)-$$}"
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
    --screen-size "${SCREEN_SIZE}" \
    --test-output-dir "${OUT}/${name}" \
    -e "BASE_URL=${BASE_URL}" \
    -e "ADMIN_USER=${ADMIN_USER}" \
    -e "ADMIN_PASSWORD=${ADMIN_PASSWORD}" \
    -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME}" \
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
