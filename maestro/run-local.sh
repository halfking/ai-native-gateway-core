#!/usr/bin/env bash
# maestro/run-local.sh —— 本地逐页浏览器验证的实跑入口（Maestro 2.11.0 + 桌面 Chrome）
#
# ┌──────────────────────────────────────────────────────────────────────────┐
# │ ★ 为什么这个脚本存在：Maestro 2.11.0 的 CLI 没有 `--env-file`，          │
# │   flow 里的 ${VAR} 也不会去读 shell 的环境变量 ⇒ 变量只能靠 `-e K=V`    │
# │   逐个传 ⇒ 口令会出现在进程 argv 里（`ps` 可见）。                      │
# │   本脚本从【仓根 .env.local】读口令 → 拼 `-e` → 调用 Maestro。           │
# │   口令本身不落在仓内任何文件里（.env.local 已被 .gitignore 覆盖）。     │
# │   代价：argv 暴露只在本机进程表里存在，不落盘、不进 git。               │
# └──────────────────────────────────────────────────────────────────────────┘
#
# 用法：
#   maestro/run-local.sh                        # 跑 01..09（不含 dev-only）
#   maestro/run-local.sh maestro/flows/05-routing.yaml   # 跑指定 flow
#   maestro/run-local.sh --with-dev             # 连 90-dev-only.yaml 一起跑
#   maestro/run-local.sh --list                 # 只列要跑的 flow
#
# 环境变量（都有默认值）：
#   BASE_URL              默认 http://127.0.0.1:8782
#   MAESTRO_DRIVER_PORT   默认 9222（自起 Chrome 的 CDP 端口）
#   MAESTRO_CHROME_BIN    默认自动探测（chromium → google-chrome → macOS Chrome.app）
#   MAESTRO_JAVA_HOME     默认取 JAVA_HOME，再退到 /usr/libexec/java_home -v 17
#
# 产物：
#   maestro/reports/<时间戳>/<flow>.log         Maestro 的 CLI 输出
#   maestro/reports/<时间戳>/<flow>/            Maestro 的调试产物（commands.json 等）
#   maestro/reports/<时间戳>/summary.txt         每个 flow 的 FAILED/WARNED 计数
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

BASE_URL="${BASE_URL:-http://127.0.0.1:8782}"
DRIVER_PORT="${MAESTRO_DRIVER_PORT:-9222}"
# ★ 视口必须覆写成桌面尺寸（2026-11-18 实测修）：Maestro 2.11.0 的 **web 驱动**
#   视口固定 1024×625，view hierarchy 只覆盖视口 ⇒ **折叠线以下的内容 `assertVisible` 一律判 false**
#   ⇒ 本脚本原先没传 `--screen-size` ⇒ 用它跑全量回归会**稳定产出假红**：
#   实测 `11-simple-overview` 的「上游健康」在 DOM 里（SimpleOverviewView.vue:702 的
#   panel-title，无 v-if）却判红 ⇒ 覆写视口后同一条 flow **F11_EXIT=0 全绿**。
#   （与 run-stage-tree.sh 头注、docs/skills/maestro-browser-verification/SKILL.md §六.2 同口径。）
SCREEN_SIZE="${MAESTRO_SCREEN_SIZE:-1440x1400}"
REPORT_ROOT="${MAESTRO_REPORT_ROOT:-${ROOT}/maestro/reports}"

# ── 1. 选 flow ────────────────────────────────────────────────────────────────
WITH_DEV=0
FLOWS=()
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

# ★ 跑法守卫：`10-mcp-gateway.yaml` **不属于本 SPA 回归集**（2026-11-18 实测修）。
#   它的头注写明：页面是**自包含 HTML（无前端构建链）**，令牌从 URL 的 `?token=` 读，
#   跑法是 `bash tests/mcp-gateway-maestro/run.sh` —— 那个脚本会**自起一个带示例上游的
#   MCP 网关**并给出 `MCP_ADMIN_TOKEN`。
#   ⇒ 拿本脚本（run-local.sh）去跑它 ⇒ `MCP_ADMIN_TOKEN` 没设 ⇒
#     `${BASE_URL}/mcp/admin?token=` 打不开 ⇒ **必然假红**（实测：「MCP 网关运营看板」不可见）。
#   ⇒ 这里**显式剔除**它，而不是把假红算进回归结论。
RUNNABLE=()
for flow in "${FLOWS[@]}"; do
  if [ "$(basename "${flow}")" = "10-mcp-gateway.yaml" ]; then
    echo "!! 跳过 ${flow}：它需要自带的 MCP 网关（tests/mcp-gateway-maestro/run.sh + MCP_ADMIN_TOKEN），" >&2
    echo "   不是本 SPA 回归集的一员；直接跑它只会产出假红。" >&2
    continue
  fi
  RUNNABLE+=("${flow}")
done
FLOWS=("${RUNNABLE[@]}")

if [ "${LIST_ONLY:-0}" = "1" ]; then
  printf '%s\n' "${FLOWS[@]}"
  exit 0
fi

# ── 2. JDK ────────────────────────────────────────────────────────────────────
# 依次探测：MAESTRO_JAVA_HOME → JAVA_HOME → macOS java_home → 常见安装位置
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
           /usr/local/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home \
           "${HOME}"/jdk*/Contents/Home \
           "${HOME}"/kaixuan/jdk*/Contents/Home \
           /Library/Java/JavaVirtualMachines/*/Contents/Home; do
    [ -x "${c}/bin/java" ] && { printf '%s' "${c}"; return; }
  done
  printf ''
}
JAVA_HOME_CANDIDATE="$(find_jdk)"
if [ -z "${JAVA_HOME_CANDIDATE}" ]; then
  echo "!! 找不到 JDK 17。请设 MAESTRO_JAVA_HOME=<jdk 根>（macOS 的 .jdk/Contents/Home）。" >&2
  exit 2
fi
export JAVA_HOME="${JAVA_HOME_CANDIDATE}"
export PATH="${JAVA_HOME}/bin:${PATH}"

if ! command -v maestro >/dev/null 2>&1; then
  echo "!! PATH 里没有 maestro。装法：curl -Ls https://get.maestro.mobile.dev | bash" >&2
  exit 2
fi

# ── 3. 凭据 ────────────────────────────────────────────────────────────────
if [ -z "${LLM_GATEWAY_ADMIN_USER:-}" ] || [ -z "${LLM_GATEWAY_ADMIN_PASSWORD:-}" ]; then
  if [ ! -f "${ROOT}/.env.local" ]; then
    echo "!! 需要 LLM_GATEWAY_ADMIN_USER / LLM_GATEWAY_ADMIN_PASSWORD 或仓根 .env.local" >&2
    exit 2
  fi
  set -a
  # shellcheck disable=SC1091
  . "${ROOT}/.env.local"
  set +a
fi
: "${LLM_GATEWAY_ADMIN_USER:?需要 LLM_GATEWAY_ADMIN_USER}"
: "${LLM_GATEWAY_ADMIN_PASSWORD:?需要 LLM_GATEWAY_ADMIN_PASSWORD}"
export BASE_URL LLM_GATEWAY_ADMIN_USER LLM_GATEWAY_ADMIN_PASSWORD
umask 077

# ── 4. Chrome（CDP） ─────────────────────────────────────────────────────────
find_chrome() {
  if [ -n "${MAESTRO_CHROME_BIN:-}" ]; then printf '%s' "${MAESTRO_CHROME_BIN}"; return; fi
  if command -v chromium >/dev/null 2>&1; then command -v chromium; return; fi
  if command -v google-chrome >/dev/null 2>&1; then command -v google-chrome; return; fi
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "/Applications/Chromium.app/Contents/MacOS/Chromium" \
           "/usr/bin/google-chrome" "/usr/bin/chromium-browser"; do
    [ -x "${c}" ] && { printf '%s' "${c}"; return; }
  done
  printf ''
}
CHROME_BIN="$(find_chrome)"
if [ -z "${CHROME_BIN}" ]; then
  echo "!! 找不到 Chrome。用 MAESTRO_CHROME_BIN 指定可执行文件。" >&2
  exit 2
fi

CHROME_PROFILE="${MAESTRO_CHROME_PROFILE:-/tmp/maestro-local-chrome-profile}"
if ! curl -s --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
  echo "== 自起 Chrome（CDP :${DRIVER_PORT}）"
  mkdir -p "${CHROME_PROFILE}"
  nohup "${CHROME_BIN}" \
    --headless=new --remote-debugging-port="${DRIVER_PORT}" \
    --user-data-dir="${CHROME_PROFILE}" \
    --no-first-run --no-default-browser-check --disable-gpu \
    about:blank >"${REPORT_ROOT}.chrome.log" 2>&1 &
  for _ in $(seq 1 30); do
    curl -s --max-time 2 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1 && break
    sleep 1
  done
  if ! curl -s --max-time 3 "http://127.0.0.1:${DRIVER_PORT}/json/version" >/dev/null 2>&1; then
    echo "!! Chrome 起不来（看 ${REPORT_ROOT}.chrome.log）" >&2
    exit 2
  fi
fi

# ── 5. 动态路由的真实 ID ────────────────────────────────────────────────────
# flows 里有 16 处动态路由参数（${PROVIDER_ID} / ${REQUEST_ID} / ${TENANT_CODE}…）。
# Maestro 的 ${} 只做字符串替换 ⇒ 不喂真值就会打开 "/providers/" 这种打不开的 URL，
# 断言必然红、而红的原因与被测页面无关。⇒ 从活着的网关上解析真值。
mkdir -p "${REPORT_ROOT}"
OUT="$(mktemp -d "${REPORT_ROOT}/$(date +%Y%m%d-%H%M%S).XXXXXX")"
ID_ARGS=()
if node "${ROOT}/maestro/tools/resolve-ids.mjs" >"${OUT}/route-ids.txt" 2>"${OUT}/route-ids.err"; then
  while IFS= read -r line; do
    case "${line}" in
      [A-Z_]*=*) ID_ARGS+=( -e "${line}" ) ;;
    esac
  done <"${OUT}/route-ids.txt"
  echo "== 动态路由 ID：$(wc -l <"${OUT}/route-ids.txt" | tr -d ' ') 个"
  [ -s "${OUT}/route-ids.err" ] && sed 's/^/   /' "${OUT}/route-ids.err"
else
  echo "!! 解析动态路由 ID 失败（flows 的动态路由会退化成空 URL）" >&2
  sed 's/^/   /' "${OUT}/route-ids.err" >&2 || true
fi

# ── 6. 逐个 flow 实跑 ────────────────────────────────────────────────────
SUMMARY="${OUT}/summary.txt"
: >"${SUMMARY}"

overall=0
for flow in "${FLOWS[@]}"; do
  name="$(basename "${flow}" .yaml)"
  echo "==================== ${name} ===================="
  set +e
  maestro test \
    --driver-host-port "${DRIVER_PORT}" \
    --headless \
    --no-ansi \
    --screen-size "${SCREEN_SIZE}" \
    --test-output-dir "${OUT}/${name}" \
    -e "BASE_URL=${BASE_URL}" \
    -e "ADMIN_USER=${LLM_GATEWAY_ADMIN_USER}" \
    -e "ADMIN_PASSWORD=${LLM_GATEWAY_ADMIN_PASSWORD}" \
    -e "ADMIN_DISPLAY_NAME=${ADMIN_DISPLAY_NAME:-系统管理员}" \
    "${ID_ARGS[@]}" \
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
echo "==== 汇总 ===="
cat "${SUMMARY}"
echo "产物目录：${OUT}"
exit "${overall}"
