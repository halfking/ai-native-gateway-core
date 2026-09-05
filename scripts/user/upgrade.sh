#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/user/upgrade.sh
# SYNC_POLICY: 修改本脚本时同步改 maintain 对应位置。service identity 由本仓库维护。
# ADAPTATIONS: 加 loong64 严格门；INSTALL_ROOT 默认智能探测；service_stop / service_start 加 macOS launchd 路径。

# upgrade.sh — 用户侧升级工具。
#
# 子命令（默认 run = 全流程）：
#   list      查看可用版本列表（/distribution/versions 或 catalog）
#   download  仅下载最新包到旁路目录（校验 sha256）
#   install   解压到 staging（不切换；复核 sha256）
#   switch    停止服务 → 备份（失败即中止）→ 切换 → 启动
#   test      健康检查（三段：/healthz + /readyz + /api/distribution/versions）
#   rollback  从最近备份回滚，并在回滚后健康检查
#   record    上报 upgrade-report（status=completed|failed|rolled_back）
#   run       完整：检查→下载→安装→切换→测试→成功上报；失败自动回滚并上报
#
# 用法:
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/upgrade" | bash -s -- list
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/upgrade" | DRY_RUN=1 bash -s -- run
#   bash upgrade.sh run
#   TARGET_VERSION=1.2.0 bash upgrade.sh download
#
# Env:
#   ALLOW_UNVERIFIED_PACKAGE  1=允许安装没有已发布 sha256 的包（默认 0，拒绝）
#   HEALTH_RETRIES            健康检查重试秒数（默认 30）
#   NO_SUDO                   1=不用 sudo（已是 root / 容器 / 测试）
#   REPORT_DUMP_ONLY          1=只打印上报 JSON 不发送（供测试断言 payload 合法性）
#   INSTALL_MODE              auto|host|compose（默认 auto）
#   COMPOSE_DIR               Compose 工作目录（默认 INSTALL_ROOT）
#   COMPOSE_FILE              Compose 文件（默认 COMPOSE_DIR/docker-compose.yml）
#   LOG_PATH                  生命周期日志（默认 INSTALL_ROOT/.upgrade.log）
#   STATE_PATH                状态文件（默认 INSTALL_ROOT/.upgrade-state.json）
#   NO_INTERACTIVE            1=禁用交互提示（CI/管道）
#   BACKUP_KEEP               保留的快照份数（默认 3；旧的自动 prune）
#   HEALTH_URL / READYZ_URL   健康/就绪端点（默认 /healthz、/readyz）
#
# 配置解析层级（与 install-host/install-docker 一致）：
#   1) 环境变量  2) ~/.kxmaint/config  3) 交互回退（仅 tty+NO_INTERACTIVE!=1+DRY_RUN!=1）  4) default
set -euo pipefail

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"

# 与 install-host.sh / install-docker.sh 共用同一份解析逻辑；不 source config 文件
# 以防注入，仅按 KEY=VALUE 行读取；已显式 export 的环境变量不被 config 覆盖。
load_config_file() {
  [[ -f "$KXMAINT_CONFIG" ]] || return 0
  while IFS= read -r line; do
    case "$line" in
      '#'*|'') continue ;;
    esac
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    local k="${BASH_REMATCH[1]}" v="${BASH_REMATCH[2]}"
    if [[ -z "${!k:-}" ]]; then
      printf -v "$k" '%s' "$v"
    fi
  done < "$KXMAINT_CONFIG"
}

save_config_file() {
  local k
  umask 077
  run_priv mkdir -p "$(dirname "$KXMAINT_CONFIG")"
  {
    printf '# kxmaint user config — written by %s on %s\n' "${0##*/}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '# DO NOT edit by hand unless you know the format.\n'
    for k in "$@"; do
      printf '%s=%s\n' "$k" "${!k}"
    done
  } > "$KXMAINT_CONFIG.tmp"
  run_priv mv "$KXMAINT_CONFIG.tmp" "$KXMAINT_CONFIG"
  run_priv chmod 600 "$KXMAINT_CONFIG"
}

prompt_value() {
  local var="$1" prompt="$2" default="$3" reply=""
  if [[ "${NO_INTERACTIVE:-0}" == "1" || "${DRY_RUN:-0}" == "1" || ! -t 0 ]]; then
    printf -v "$var" '%s' "$default"
    return 0
  fi
  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " reply || true
    [[ -n "$reply" ]] || reply="$default"
  else
    read -r -p "$prompt: " reply || true
    [[ -n "$reply" ]] || { echo "[kxmaint] $prompt cannot be empty" >&2; return 1; }
  fi
  printf -v "$var" '%s' "$reply"
}

run_priv() {
  if [[ "${NO_SUDO:-0}" == "1" || "$(id -u)" == "0" ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

load_config_file

# 在 default 之前用交互补齐——同时供后续 validate 复用。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgo.kxpms.cn/maintain-api"
fi

MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
INSTALL_ROOT="${INSTALL_ROOT:-/opt/llm-gateway}"
INSTANCE_ID="${INSTANCE_ID:-$(cat "${INSTALL_ROOT}/instance_id" 2>/dev/null || true)}"
CURRENT_VERSION="${CURRENT_VERSION:-$(cat "${INSTALL_ROOT}/VERSION" 2>/dev/null || echo v0.0.0)}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:8080/healthz}"
# 第二阶段就绪探针：/readyz 通常在启动后才会返回 200，用来区分"进程在跑"与
# "已经处理完自检并能对外服务"。
READYZ_URL="${READYZ_URL:-${HEALTH_URL%/healthz}/readyz}"
# 第三阶段版本端点：必须能解析出与新二进制对应的版本号——如果切完后还能
# 解析到旧版本，说明回滚/启动并未真正生效。
VERSION_PROBE_URL="${VERSION_PROBE_URL:-${MAINTAIN_BASE%/maintain-api}/maintain/version}"
HEALTH_RETRIES="${HEALTH_RETRIES:-30}"
DRY_RUN="${DRY_RUN:-0}"
TARGET_VERSION="${TARGET_VERSION:-}"
# Accept either VERSION or TARGET_VERSION so the variable name matches
# install-host.sh / install-docker.sh (TARGET_VERSION is the older spelling).
# Either form may be set; we keep them in sync so downstream code that reads
# TARGET_VERSION keeps working.
VERSION="${VERSION:-${TARGET_VERSION:-}}"
: "${TARGET_VERSION:=${VERSION}}"
# Validate VERSION (semver + build_seq; same character set as the UI's
# commandBuilder.ts allowlist and the other install scripts).
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[upgrade] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi
# 没有已发布 sha256 时是否允许安装。默认 0：拒绝装未校验的二进制。
ALLOW_UNVERIFIED_PACKAGE="${ALLOW_UNVERIFIED_PACKAGE:-0}"
STAGING="${INSTALL_ROOT}/.upgrade-staging"
INSTALL_MODE="${INSTALL_MODE:-auto}"
COMPOSE_DIR="${COMPOSE_DIR:-${INSTALL_ROOT}}"
COMPOSE_FILE="${COMPOSE_FILE:-${COMPOSE_DIR}/docker-compose.yml}"
LOG_PATH="${LOG_PATH:-${INSTALL_ROOT}/.upgrade.log}"
STATE_PATH="${STATE_PATH:-${INSTALL_ROOT}/.upgrade-state.json}"
ROLLBACK_SCRIPT="${ROLLBACK_SCRIPT:-sudo bash ${INSTALL_ROOT}/scripts/upgrade.sh rollback}"
CMD="${1:-run}"
# 升级前快照保留份数；超过则按 mtime 升序剪枝。默认 3。
BACKUP_KEEP="${BACKUP_KEEP:-3}"

case "$INSTALL_MODE" in
  auto|host|compose) ;;
  *) echo "[upgrade] invalid INSTALL_MODE=${INSTALL_MODE} (auto|host|compose)" >&2; exit 2 ;;
esac

arch="${ARCH:-$(uname -m)}"
case "$arch" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  loongarch64|loong64) ARCH=loong64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 2 ;;
esac
# loong64 默认严格：未显式 LOONG64_OK=1 时直接退出（CI 默认不发 loong64 artifact）。
if [[ "$ARCH" == "loong64" && "${LOONG64_OK:-0}" != "1" ]]; then
  echo "[upgrade] loongarch64 默认不启用（CI 默认不发 loong64 artifact）。如需安装请设 LOONG64_OK=1。" >&2
  exit 2
fi

LATEST=""
URL=""
SHA=""
FILE=""
UPDATE_AVAILABLE=""

# run_priv / load_config_file 等辅助函数在脚本顶部已经定义。

LOG_AVAILABLE=0
INSTALL_MODE_EFFECTIVE="host"

log_event() {
  [[ "$LOG_AVAILABLE" == "1" ]] || return 0
  printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | run_priv tee -a "$LOG_PATH" >/dev/null || true
}

write_state() {
  local status="$1" backup="${2:-}" target="${3:-${LATEST:-}}" tmp
  local state_dir
  state_dir="$(dirname "$STATE_PATH")"
  if ! run_priv mkdir -p "$state_dir" 2>/dev/null || ! run_priv touch "$LOG_PATH" 2>/dev/null; then
    echo "[upgrade] WARN state/log path unavailable: state=$STATE_PATH log=$LOG_PATH (stdout/stderr remain the log source)" >&2
    return 0
  fi
  tmp="${TMPDIR:-/tmp}/upgrade-state.$$"
  if ! STATE_TMP="$tmp" S_MODE="$INSTALL_MODE_EFFECTIVE" \
    S_BACKUP="$backup" S_ROLLBACK="$ROLLBACK_SCRIPT" S_LOG="$LOG_PATH" \
    S_LOG_AVAILABLE="$LOG_AVAILABLE" S_CURRENT="$CURRENT_VERSION" S_TARGET="$target" \
    S_STATUS="$status" python3 -c '
import json, os
with open(os.environ["STATE_TMP"], "w", encoding="utf-8") as f:
    json.dump({
        "install_mode": os.environ["S_MODE"],
        "backup_dir": os.environ["S_BACKUP"],
        "rollback_script": os.environ["S_ROLLBACK"],
        "log_path": os.environ["S_LOG"],
        "log_available": os.environ["S_LOG_AVAILABLE"] == "1",
        "current_version": os.environ["S_CURRENT"],
        "target_version": os.environ["S_TARGET"],
        "status": os.environ["S_STATUS"],
    }, f, ensure_ascii=False, indent=2)
    f.write("\n")
'; then
    echo "[upgrade] WARN cannot write state $STATE_PATH (stdout/stderr remain the log source)" >&2
    rm -f "$tmp"
    return 0
  fi
  if ! run_priv mv "$tmp" "$STATE_PATH"; then
    echo "[upgrade] WARN cannot install state $STATE_PATH (stdout/stderr remain the log source)" >&2
    rm -f "$tmp"
    return 0
  fi
  log_event "state status=$status mode=$INSTALL_MODE_EFFECTIVE backup=${backup:-none}"
}

compose_available() {
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1
}

compose_uses_install_root() {
  [[ -f "$COMPOSE_FILE" ]] || return 1
  [[ "${COMPOSE_MANAGED_ROOT:-0}" == "1" ]] || grep -F -- "$INSTALL_ROOT" "$COMPOSE_FILE" >/dev/null 2>&1
}

detect_install_mode() {
  case "$INSTALL_MODE" in
    host) INSTALL_MODE_EFFECTIVE="host" ;;
    compose)
      [[ -f "$COMPOSE_FILE" ]] || { echo "[upgrade] compose mode requires $COMPOSE_FILE" >&2; return 1; }
      compose_available || { echo "[upgrade] compose mode requires docker compose" >&2; return 1; }
      INSTALL_MODE_EFFECTIVE="compose"
      ;;
    auto)
      if [[ -f "$COMPOSE_FILE" ]]; then
        compose_available || { echo "[upgrade] found $COMPOSE_FILE but docker compose is unavailable" >&2; return 1; }
        INSTALL_MODE_EFFECTIVE="compose"
      else
        INSTALL_MODE_EFFECTIVE="host"
      fi
      ;;
  esac
}

validate_compose_upgrade() {
  [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]] || return 0
  if ! compose_uses_install_root; then
    echo "[upgrade] refusing host-package switch for image-only Compose project: $COMPOSE_FILE" >&2
    echo "[upgrade] use install-docker.sh to load the published image tar, then run docker compose up -d" >&2
    return 1
  fi
}

compose_exec() {
  (cd "$COMPOSE_DIR" && run_priv docker compose -f "$COMPOSE_FILE" "$@")
}

init_logging() {
  if run_priv mkdir -p "$(dirname "$LOG_PATH")" 2>/dev/null && run_priv touch "$LOG_PATH" 2>/dev/null; then
    LOG_AVAILABLE=1
    log_event "upgrade command=$CMD mode=${INSTALL_MODE_EFFECTIVE}"
  else
    echo "[upgrade] WARN cannot create $LOG_PATH; stdout/stderr are the log source" >&2
  fi
}

sha256_of() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    return 1
  fi
}

# 校验失败必须返回非 0：以前 `echo "$SHA  $pkg" | sha256sum -c -` 只在 SHA 非空时跑，
# 而定向升级路径把 SHA 清空了，等于整条路径无校验。
verify_sha256() {
  local expected="$1" file="$2" actual=""
  if ! actual="$(sha256_of "$file")"; then
    echo "[upgrade] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  if [[ "${actual,,}" != "${expected,,}" ]]; then
    echo "[upgrade] checksum MISMATCH $file" >&2
    echo "[upgrade]   expected=$expected" >&2
    echo "[upgrade]   actual  =$actual" >&2
    return 1
  fi
  echo "[upgrade] sha256 OK $(basename "$file")"
}

report() {
  local status="$1" err="${2:-}"
  [[ -n "$INSTANCE_ID" ]] || { echo "[upgrade] skip report (no INSTANCE_ID) status=$status"; return 0; }
  # error 来自任意命令输出，可能带引号/换行/反斜杠；字符串拼接会产出非法 JSON
  # 或注入额外字段。统一交给 json.dumps（同 scripts/release-pipeline.sh）。
  local body
  if ! body="$(R_INSTANCE="$INSTANCE_ID" R_FROM="$CURRENT_VERSION" R_TO="${LATEST:-}" \
    R_STATUS="$status" R_ERR="$err" python3 -c '
import json, os
print(json.dumps({
    "instance_id": os.environ["R_INSTANCE"],
    "from_version": os.environ["R_FROM"],
    "to_version": os.environ["R_TO"],
    "status": os.environ["R_STATUS"],
    "error": os.environ["R_ERR"],
}, ensure_ascii=False))
')"; then
    echo "[upgrade] report payload build failed (status=$status)" >&2
    return 0
  fi
  [[ "${REPORT_DUMP_ONLY:-0}" != "1" ]] || { printf '%s\n' "$body"; return 0; }
  # Public distribution endpoint (no customer JWT required); agent auth route also exists.
  # 上报是观测信息，不能顶掉真正的升级结果 —— 失败只告警。
  if ! curl -fsSL -X POST "${MAINTAIN_BASE}/distribution/upgrade-report" \
    -H 'Content-Type: application/json' \
    -H "X-License-Key: ${LICENSE_KEY:-}" \
    -H "X-Hardware-Hash: ${HARDWARE_HASH:-}" \
    -d "$body" >/dev/null 2>&1; then
    echo "[upgrade] WARN report failed (status=$status) — 结果不受影响" >&2
    return 0
  fi
  echo "[upgrade] reported status=$status"
}

# ticket 接口只返回 url/file_name/expires_at/request_id，没有 sha256。
# 定向升级要校验，就必须回到目录接口取已发布的 sha256（CatalogItem.sha256）。
published_sha() {
  local version="$1" json=""
  if ! json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)"; then
    json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  fi
  LOOKUP_JSON="$json" WANT_VERSION="$version" WANT_PLATFORM=linux WANT_ARCH="$ARCH" python3 - <<'PY'
import json, os
try:
    d = json.loads(os.environ["LOOKUP_JSON"])
except Exception:
    raise SystemExit(0)
want_v = os.environ["WANT_VERSION"].lstrip("v")
want_p = os.environ["WANT_PLATFORM"]
want_a = os.environ["WANT_ARCH"]
for v in (d.get("items") or d.get("versions") or []):
    if not isinstance(v, dict):
        continue
    if (v.get("version") or "").lstrip("v") != want_v:
        continue
    for a in (v.get("items") or v.get("artifacts") or []):
        if isinstance(a, dict) and a.get("platform") == want_p and a.get("arch") == want_a:
            print(a.get("sha256") or "")
            raise SystemExit(0)
PY
}

fetch_check() {
  local current="${CURRENT_VERSION}"
  # Asking for a specific TARGET_VERSION still uses version-check against current
  # to obtain signed storage_uri for the latest matching platform; if target is
  # set and differs from latest we fall back to ticket.
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=${current}&platform=linux&arch=${ARCH}")"
  CHECK_JSON="$CHECK_JSON" TARGET_VERSION="$TARGET_VERSION" python3 - <<'PY' >"${TMPDIR:-/tmp}/.upgrade-parse.$$"
import json, os
d = json.loads(os.environ["CHECK_JSON"])
target = (os.environ.get("TARGET_VERSION") or "").strip().lstrip("v")
arts = d.get("target_artifacts") or []
art = arts[0] if arts else {}
latest = (d.get("latest_version") or "").lstrip("v")
avail = "true" if d.get("update_available") else "false"
if target:
    # Pin target; treat as available when target != current.
    current = (d.get("current_version") or "").lstrip("v")
    avail = "true" if target and target != current else "false"
    latest = target
print(avail)
print(latest)
print(art.get("storage_uri") or "")
print(art.get("sha256") or "")
print(art.get("artifact_name") or art.get("filename") or "")
PY
  mapfile -t _f < "${TMPDIR:-/tmp}/.upgrade-parse.$$"
  rm -f "${TMPDIR:-/tmp}/.upgrade-parse.$$"
  UPDATE_AVAILABLE="${_f[0]:-false}"
  LATEST="${_f[1]:-}"
  URL="${_f[2]:-}"
  SHA="${_f[3]:-}"
  FILE="${_f[4]:-}"

  if [[ -n "$TARGET_VERSION" && ( -z "$URL" || "${LATEST#v}" != "${TARGET_VERSION#v}" ) ]]; then
    local ver="${TARGET_VERSION#v}"
    local ticket ticket_body
    ticket_body="$(TV="$ver" TA="$ARCH" python3 -c '
import json, os
print(json.dumps({"version": os.environ["TV"], "platform": "linux", "arch": os.environ["TA"]}))
')"
    ticket="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" -H 'Content-Type: application/json' \
      -d "$ticket_body")"
    URL="$(printf '%s' "$ticket" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("url",""))')"
    FILE="$(printf '%s' "$ticket" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("file_name",""))')"
    LATEST="$ver"
    UPDATE_AVAILABLE=true
    # ticket 没有 sha256 —— 从目录接口补齐，而不是把校验值清空。
    SHA="$(published_sha "$ver" || true)"
  fi
}

cmd_list() {
  echo "[upgrade] available versions (channel=${CHANNEL})"
  LIST_JSON=""
  if LIST_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)"; then
    :
  else
    LIST_JSON="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog")"
  fi
  LIST_JSON="$LIST_JSON" python3 - <<'PY'
import json, os
raw = os.environ.get("LIST_JSON") or "{}"
try:
    d = json.loads(raw)
except Exception as e:
    print("  (parse failed)", e)
    raise SystemExit(0)
items = d.get("items") or d.get("versions") or []
for v in items:
    if not isinstance(v, dict):
        print(" ", v)
        continue
    ver = v.get("version") or ""
    seq = v.get("build_seq", "")
    ch = v.get("channel", "")
    arts = v.get("artifacts") or v.get("items") or []
    parts = []
    for a in arts:
        if not isinstance(a, dict):
            continue
        plat = a.get("platform") or "?"
        arch = a.get("arch") or "?"
        size = a.get("size_label") or a.get("size_bytes") or "?"
        parts.append(f"{plat}/{arch}:{size}")
    sizes = ", ".join(parts) or "-"
    print(f"  {ver}  build={seq}  channel={ch}  [{sizes}]")
PY
  fetch_check
  echo "[upgrade] current=${CURRENT_VERSION} latest=${LATEST:-?} update_available=${UPDATE_AVAILABLE:-false}"
}

# 无已发布 sha256 时的显式决定：默认拒绝，ALLOW_UNVERIFIED_PACKAGE=1 才放行且大声告警。
require_sha_decision() {
  local ver="$1"
  if [[ -n "$SHA" ]]; then
    return 0
  fi
  if [[ "$ALLOW_UNVERIFIED_PACKAGE" == "1" ]]; then
    echo "[upgrade] ############################################################" >&2
    echo "[upgrade] WARNING: ${ver} 没有已发布 sha256，按 ALLOW_UNVERIFIED_PACKAGE=1 继续" >&2
    echo "[upgrade] WARNING: 本次升级的安装包完整性未经校验" >&2
    echo "[upgrade] ############################################################" >&2
    return 0
  fi
  echo "[upgrade] refusing unverified package: ${ver} 无已发布 sha256" >&2
  echo "[upgrade] 请让发布方补登记 sha256，或显式设置 ALLOW_UNVERIFIED_PACKAGE=1 承担风险" >&2
  return 1
}

cmd_download() {
  fetch_check
  if [[ "$UPDATE_AVAILABLE" != "true" ]]; then
    echo "[upgrade] already up to date (${LATEST:-$CURRENT_VERSION})"
    return 0
  fi
  [[ -n "$URL" ]] || { echo "missing download uri" >&2; exit 1; }
  echo "[upgrade] download ${CURRENT_VERSION} -> ${LATEST} file=${FILE}"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 url=$URL sha256=${SHA:-<none>}"
    return 0
  fi
  # 校验决定放在下载之前：没有 sha 又没开开关，就不该把包落到盘上。
  require_sha_decision "${LATEST}" || exit 1
  run_priv mkdir -p "$STAGING"
  local pkg="${STAGING}/.package.tar.gz"
  curl -fsSL "$URL" -o "$pkg"
  if [[ -n "$SHA" ]]; then
    if ! verify_sha256 "$SHA" "$pkg"; then
      run_priv rm -f "$pkg"
      echo "[upgrade] 已删除校验失败的包，未改动任何已安装文件" >&2
      exit 1
    fi
    printf '%s\n' "$SHA" | run_priv tee "${STAGING}/.package_sha256" >/dev/null
  else
    printf '%s\n' "UNVERIFIED" | run_priv tee "${STAGING}/.package_sha256" >/dev/null
  fi
  printf '%s\n' "$LATEST" | run_priv tee "${STAGING}/.target_version" >/dev/null
  printf '%s\n' "${FILE:-pkg.tar.gz}" | run_priv tee "${STAGING}/.package_name" >/dev/null
  echo "[upgrade] package saved to $pkg"
}

cmd_install() {
  local pkg="${STAGING}/.package.tar.gz"
  [[ -f "$pkg" ]] || { echo "run download first (missing $pkg)" >&2; exit 1; }
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would extract $pkg -> $STAGING"
    return 0
  fi
  # 复核：download 与 install 可能是两次独立调用，包在中间被换掉也要拦住。
  local recorded
  recorded="$(cat "${STAGING}/.package_sha256" 2>/dev/null || true)"
  if [[ -z "$recorded" ]]; then
    echo "[upgrade] missing ${STAGING}/.package_sha256 — 请重新执行 download" >&2
    exit 1
  fi
  if [[ "$recorded" == "UNVERIFIED" ]]; then
    if [[ "$ALLOW_UNVERIFIED_PACKAGE" != "1" ]]; then
      echo "[upgrade] staged package is unverified — 拒绝安装（设 ALLOW_UNVERIFIED_PACKAGE=1 才放行）" >&2
      exit 1
    fi
    echo "[upgrade] WARNING installing unverified package (ALLOW_UNVERIFIED_PACKAGE=1)" >&2
  else
    verify_sha256 "$recorded" "$pkg" || exit 1
  fi
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || true)"
  run_priv mkdir -p "${STAGING}/content"
  run_priv rm -rf "${STAGING}/content/"*
  run_priv tar -xzf "$pkg" -C "${STAGING}/content" --strip-components=1 2>/dev/null \
    || run_priv tar -xzf "$pkg" -C "${STAGING}/content"
  echo "[upgrade] staged content in ${STAGING}/content"
}

latest_backup() {
  ls -1d "${INSTALL_ROOT}"/.upgrade-backup-* 2>/dev/null | sort | tail -1 || true
}

# 备份失败必须中止：以前是 `cp -a ... || true`，等于在没有回滚材料的情况下
# 继续 rsync --delete 覆盖线上安装。
make_backup() {
  local backup="$1"
  if ! command -v rsync >/dev/null 2>&1; then
    echo "[upgrade] backup FAILED: rsync required" >&2
    return 1
  fi
  if ! run_priv mkdir -p "$backup"; then
    echo "[upgrade] backup FAILED: cannot create $backup" >&2
    return 1
  fi
  # 备份目录就在 INSTALL_ROOT 里面，所以必须排除 .upgrade-*，否则会把备份
  # 目录复制进它自己。原来用的是 `cp -a "$INSTALL_ROOT"/. "$backup"/ || true`：
  # GNU cp 会报 "cannot copy a directory into itself" 并返回非 0，BSD cp 直接
  # 递归到路径超长 —— 两种情况都被 `|| true` 吞掉，"备份成功"从未被验证过。
  if ! run_priv rsync -a --exclude '.upgrade-*' "$INSTALL_ROOT"/ "$backup"/; then
    echo "[upgrade] backup FAILED: rsync $INSTALL_ROOT -> $backup" >&2
    return 1
  fi
  # 空备份等于没有备份。
  if [[ -z "$(ls -A "$backup" 2>/dev/null || true)" ]]; then
    echo "[upgrade] backup FAILED: $backup is empty" >&2
    return 1
  fi
  # 逐项复核顶层条目（.upgrade-* 是工具自身目录，不参与）。
  local entry name missing=0
  for entry in "$INSTALL_ROOT"/*; do
    [[ -e "$entry" ]] || continue
    name="$(basename "$entry")"
    case "$name" in .upgrade-*) continue ;; esac
    if [[ ! -e "${backup}/${name}" ]]; then
      echo "[upgrade] backup FAILED: missing ${name} in $backup" >&2
      missing=1
    fi
  done
  [[ "$missing" == "0" ]] || return 1
  echo "[upgrade] backup -> $backup (verified)"
}

service_stop() {
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    echo "[upgrade] stopping compose project ${COMPOSE_FILE}"
    compose_exec stop
    return
  fi
  # Linux + systemd
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -E -q '^llm-gateway-go(\.service)?'; then
    run_priv systemctl stop llm-gateway-go
    return
  fi
  # macOS + launchd
  if command -v launchctl >/dev/null 2>&1 && [[ "$(uname -s)" == "Darwin" ]]; then
    local plist="/Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist"
    if [[ -f "$plist" ]]; then
      run_priv launchctl bootout system "$plist" 2>/dev/null || true
    fi
  fi
}

service_start() {
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    echo "[upgrade] recreating compose project ${COMPOSE_FILE}"
    compose_exec up -d --force-recreate
    return
  fi
  # Linux + systemd
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -E -q '^llm-gateway-go(\.service)?'; then
    run_priv systemctl start llm-gateway-go
    return
  fi
  # macOS + launchd
  if command -v launchctl >/dev/null 2>&1 && [[ "$(uname -s)" == "Darwin" ]]; then
    local plist="/Library/LaunchDaemons/com.kaixuan.llm-gateway-go.plist"
    if [[ -f "$plist" ]]; then
      run_priv launchctl bootstrap system "$plist"
    fi
  fi
}

cmd_switch() {
  [[ -d "${STAGING}/content" ]] || { echo "run install first" >&2; return 1; }
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || echo "$LATEST")"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would switch to ${LATEST}"
    return 0
  fi
  if ! command -v rsync >/dev/null 2>&1; then
    echo "rsync required for switch" >&2
    return 1
  fi
  # shellcheck disable=SC2155 # mask is acceptable: $INSTALL_ROOT is always set at this point and a stale path would only fail downstream.
  local BACKUP="${INSTALL_ROOT}/.upgrade-backup-$(date +%Y%m%d%H%M%S)-$$"
  detect_install_mode || return 1
  validate_compose_upgrade || return 1
  init_logging
  write_state prepared "$BACKUP" "$LATEST"
  if ! run_priv mkdir -p "$INSTALL_ROOT"; then
    echo "[upgrade] cannot create $INSTALL_ROOT" >&2
    return 1
  fi
  # 备份在停服之前做，且失败就退出 —— 此时线上安装还未被触碰。
  # shellcheck disable=SC2010 # ls/grep is fine here: we filter literal `.upgrade*` names, filenames are well-known
  if [[ -n "$(ls -A "$INSTALL_ROOT" 2>/dev/null | grep -v '^\.upgrade' || true)" ]]; then
    if ! make_backup "$BACKUP"; then
      echo "[upgrade] aborting switch: 没有可用备份，不会覆盖 ${INSTALL_ROOT}" >&2
      return 1
    fi
  else
    echo "[upgrade] ${INSTALL_ROOT} 为空 — 首次安装，无需备份"
  fi
  service_stop
  if ! run_priv rsync -a --delete --exclude '.upgrade-*' "${STAGING}/content"/ "$INSTALL_ROOT"/; then
    echo "[upgrade] rsync FAILED — 安装目录可能处于半更新状态" >&2
    return 1
  fi
  service_start
  write_state switched "$BACKUP" "$LATEST"
  prune_old_backups
  echo "[upgrade] switched; service restarted if available (mode=${INSTALL_MODE_EFFECTIVE})"
}

cmd_test() {
  # 三段健康检查：/healthz（进程在跑）+ /readyz（自检完成，可服务）+
  # /version（端点解析出的版本号必须等于切完后的 LATEST）。
  # 任意一段失败都让 cmd_run 触发回滚路径 —— 这是 W8 第二轮审计的硬性增强。
  local failures=0
  local stage label url ok
  for stage in healthz readyz version; do
    case "$stage" in
      healthz)  label="/healthz"; url="$HEALTH_URL" ;;
      readyz)   label="/readyz";  url="$READYZ_URL" ;;
      version)
        # 版本探针：解析出的版本必须等于 LATEST（去掉 v 前缀、大小写不敏感）。
        # 之前切完只查 /healthz，起一个会回 200 的旧二进制也被判定为成功。
        label="/version"; url="$VERSION_PROBE_URL"
        if [[ -z "${LATEST:-}" ]]; then
          echo "[upgrade] $label: skipped (no LATEST recorded)" >&2
          failures=$((failures + 1))
          continue
        fi
        local body want_have
        body="$(curl -fsS "$url" 2>/dev/null || true)"
        if [[ -z "$body" ]]; then
          echo "[upgrade] $label: empty/non-2xx from $url" >&2
          failures=$((failures + 1))
          continue
        fi
        # 既支持顶层 version 字段，也支持 service_version/build_version 兼容字段。
        local got
        got="$(printf '%s' "$body" | python3 -c '
import json, sys
try:
    d = json.loads(sys.stdin.read() or "{}")
except Exception:
    raise SystemExit(0)
for k in ("version", "service_version", "build_version"):
    v = d.get(k)
    if isinstance(v, str) and v:
        print(v)
        raise SystemExit(0)
')"
        if [[ -z "$got" ]]; then
          echo "[upgrade] $label: no version field in response" >&2
          failures=$((failures + 1))
          continue
        fi
        want_have="${LATEST#v}"
        if [[ "${got,,}" != "${want_have,,}" ]]; then
          echo "[upgrade] $label: reported=$got expected=$want_have" >&2
          failures=$((failures + 1))
          continue
        fi
        echo "[upgrade] $label OK ($got matches LATEST=$want_have)"
        continue
        ;;
    esac
    ok=0
    for _ in $(seq 1 "$HEALTH_RETRIES"); do
      if curl -fsS "$url" >/dev/null 2>&1; then ok=1; break; fi
      sleep 1
    done
    if [[ "$ok" == "1" ]]; then
      echo "[upgrade] $label OK ($url)"
    else
      echo "[upgrade] $label FAILED ($url)" >&2
      failures=$((failures + 1))
    fi
  done
  if [[ "$failures" == "0" ]]; then
    return 0
  fi
  echo "[upgrade] health check FAILED ($failures stage(s)) — rollback will be triggered by cmd_run" >&2
  return 1
}

# W8 快照轮转：保留 BACKUP_KEEP 份最近成功的快照，按 mtime 升序剪枝。
# 失败/回滚过的快照也参与轮转（prefix .upgrade-backup-*），但若目录里有
# 任何写回的 state 标记（.rolled-back），就不动它 —— 让运维回溯更直观。
prune_old_backups() {
  local keep="${BACKUP_KEEP:-3}"
  local all=() old=() entry name
  # -1d 列出所有备份目录，按 mtime 升序。
  while IFS= read -r entry; do
    [[ -d "$entry" ]] || continue
    name="$(basename "$entry")"
    [[ "$name" == .upgrade-backup-* ]] || continue
    all+=("$entry")
  done < <(ls -1dt "${INSTALL_ROOT}"/.upgrade-backup-* 2>/dev/null | tac)
  # 超出 keep 的剪掉；不递归 —— 只动备份目录本身。
  if (( ${#all[@]} > keep )); then
    old=("${all[@]:keep}")
    for entry in "${old[@]}"; do
      echo "[upgrade] pruning old backup $entry (keep=$keep)"
      run_priv rm -rf "$entry" || true
    done
  fi
}

# 从备份恢复。返回 0=恢复命令成功，非 0=恢复本身失败（不代表健康）。
restore_from_backup() {
  local backup="$1"
  service_stop
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    # Compose projects managed by this script are bind-mounted to INSTALL_ROOT;
    # restoring files followed by recreate is the only safe rollback operation.
    if ! run_priv rsync -a --delete --exclude '.upgrade-*' "$backup"/ "$INSTALL_ROOT"/; then
      echo "[upgrade] rollback rsync FAILED from $backup" >&2
      return 1
    fi
  elif ! run_priv rsync -a --delete --exclude '.upgrade-*' "$backup"/ "$INSTALL_ROOT"/; then
    echo "[upgrade] rollback rsync FAILED from $backup" >&2
    return 1
  fi
  service_start
  write_state rolled_back "$backup" "$LATEST"
}

cmd_rollback() {
  local BACKUP
  detect_install_mode || exit 1
  validate_compose_upgrade || exit 1
  init_logging
  BACKUP="$(latest_backup)"
  [[ -n "$BACKUP" && -d "$BACKUP" ]] || { echo "no backup found under ${INSTALL_ROOT}/.upgrade-backup-*" >&2; exit 1; }
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would rollback from $BACKUP"
    return 0
  fi
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || true)"
  local base
  base="$(basename "$BACKUP")"
  if ! restore_from_backup "$BACKUP"; then
    report failed "manual rollback from ${base} failed: rsync error"
    echo "[upgrade] rollback FAILED from $BACKUP — 安装目录未恢复" >&2
    return 1
  fi
  # 回滚也要证明自己成功了：以前回滚完直接报 rolled_back，
  # 服务起不来时用户看到的是"成功"。
  if cmd_test; then
    report rolled_back "manual rollback from ${base}"
    echo "[upgrade] rolled back from $BACKUP and healthy"
    return 0
  fi
  report failed "manual rollback from ${base} restored files but health check failed (${HEALTH_URL})"
  echo "[upgrade] rollback attempted from $BACKUP but service is STILL UNHEALTHY ($HEALTH_URL)" >&2
  return 1
}

cmd_record() {
  local status="${2:-completed}"
  local err="${3:-}"
  detect_install_mode || exit 1
  init_logging
  LATEST="${LATEST:-$(cat "${STAGING}/.target_version" 2>/dev/null || true)}"
  write_state "$status" "$(latest_backup)" "$LATEST"
  fetch_check || true
  LATEST="${LATEST:-$(cat "${STAGING}/.target_version" 2>/dev/null || true)}"
  report "$status" "$err"
}

cmd_run() {
  echo "[upgrade] current=${CURRENT_VERSION} channel=${CHANNEL}"
  fetch_check
  if [[ "$UPDATE_AVAILABLE" != "true" ]]; then
    echo "[upgrade] already up to date (${LATEST:-$CURRENT_VERSION})"
    exit 0
  fi
  [[ -n "$URL" ]] || { echo "missing download uri" >&2; exit 1; }
  echo "[upgrade] ${CURRENT_VERSION} -> ${LATEST}"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 url=$URL sha256=${SHA:-<none>}"
    exit 0
  fi
  cmd_download
  cmd_install
  # switch 在覆盖前已确认备份可用；它失败时要么什么都没动，
  # 要么已有备份可回滚 —— 两种情况都不能报成功。
  if ! cmd_switch; then
    local BACKUP
    BACKUP="$(latest_backup)"
    if [[ -n "$BACKUP" && -d "$BACKUP" ]]; then
      echo "[upgrade] switch failed — rolling back from $BACKUP" >&2
      if restore_from_backup "$BACKUP" && cmd_test; then
        report rolled_back "switch failed; rolled back and healthy"
      else
        report failed "switch failed and rollback did not restore a healthy service"
      fi
    else
      report failed "switch failed before backup existed; install untouched"
    fi
    exit 1
  fi
  if ! cmd_test; then
    echo "[upgrade] health check failed — rolling back" >&2
    local BACKUP
    BACKUP="$(latest_backup)"
    if [[ -n "$BACKUP" && -d "$BACKUP" ]]; then
      if restore_from_backup "$BACKUP" && cmd_test; then
        report rolled_back "health check failed after switch"
      else
        report failed "health check failed after switch; rollback did not restore a healthy service"
      fi
    else
      report failed "health check failed; no backup"
    fi
    exit 1
  fi
  printf '%s\n' "$LATEST" | run_priv tee "$INSTALL_ROOT/VERSION" >/dev/null
  report completed
  echo "[upgrade] success ${CURRENT_VERSION} -> ${LATEST}"
}

case "$CMD" in
  list) detect_install_mode || exit 1; init_logging; cmd_list ;;
  download) detect_install_mode || exit 1; init_logging; cmd_download ;;
  install) detect_install_mode || exit 1; init_logging; cmd_install ;;
  switch) cmd_switch ;;
  test) detect_install_mode || exit 1; init_logging; cmd_test ;;
  rollback) cmd_rollback ;;
  record) cmd_record "$@" ;;
  run|full|"") cmd_run ;;
  -h|--help|help)
    sed -n '2,26p' "$0"
    ;;
  *)
    echo "unknown command: $CMD (list|download|install|switch|test|rollback|record|run)" >&2
    exit 2
    ;;
esac

# 子命令成功跑完后，把可复用的解析结果写回 ~/.kxmaint/config（mode 600）。
# 包含 MAINTAIN_BASE / CHANNEL / INSTALL_ROOT / INSTALL_MODE / COMPOSE_DIR / BACKUP_KEEP。
# 不写一次性值（VERSION / TARGET_VERSION / HEALTH_RETRIES / LICENSE_KEY）。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "${DRY_RUN:-0}" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL INSTALL_ROOT INSTALL_MODE COMPOSE_DIR BACKUP_KEEP || true
}
persist_config
