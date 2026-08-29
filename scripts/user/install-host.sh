#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/user/install-host.sh
# SYNC_POLICY: 修改本脚本时同步改 maintain 对应位置。service identity (binary 名 / user / group / install 路径) 由本仓库维护。
# ADAPTATIONS: INSTALL_ROOT 默认值改为智能探测（macOS=~/Downloads/llm-gateway-files、Linux=/opt/llm-gateway、Windows=D:/kaixuan/llm-gateway）。
#
# install-host.sh — 在主机上安装最新稳定版 llm-gateway-go 离线包。
# 用法:
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/host" | bash
#   或: bash install-host.sh
#
# 配置解析层级（高→低，后写覆盖先写）：
#   1) 进程环境变量（MAINTAIN_BASE=... CHANNEL=... 等）
#   2) ~/.kxmaint/config（首次自动写回，模式 600）
#   3) 交互式回退（仅 stdin 是 tty 且 NO_INTERACTIVE!=1 且 DRY_RUN!=1 时）
#   4) 内置 default（MAINTAIN_BASE=https://llmgo.kxpms.cn/maintain-api 等）
#
# 关闭交互：NO_INTERACTIVE=1；跳过网络：DRY_RUN=1。
set -euo pipefail

# 智能探测默认 INSTALL_ROOT。canonical 实现见 scripts/user/lib/common.sh;
# 这里内嵌一份是为了保持 curl | bash 直接调用时仍能跑（curl stdin 时 BASH_SOURCE[0]=/dev/stdin，
# 无法用 `source "$(dirname ...)/lib/common.sh"`）。
# 决策矩阵：
#   macOS  : ~/Downloads/llm-gateway-files → ~/Downloads 不可写 → ~/.local/llm-gateway
#   Linux  : /opt/llm-gateway             → /opt 不可写 → ~/.local/llm-gateway
#   Windows: D:/kaixuan/llm-gateway        → D: 不可写 → C:/llm-gateway
# INSTALL_ROOT 环境变量始终最高优先级。
detect_install_root() {
  if [[ -n "${INSTALL_ROOT:-}" ]]; then
    printf '%s\n' "$INSTALL_ROOT"
    return 0
  fi
  case "$(uname -s)" in
    Darwin*)
      if [[ -w "$HOME/Downloads" ]]; then
        printf '%s\n' "$HOME/Downloads/llm-gateway-files"
      else
        printf '%s\n' "$HOME/.local/llm-gateway"
      fi ;;
    Linux*)
      if [[ -w /opt ]]; then
        printf '/opt/llm-gateway\n'
      else
        printf '%s\n' "$HOME/.local/llm-gateway"
      fi ;;
    MINGW*|MSYS*|CYGWIN*)
      if [[ -d /d && -w /d ]]; then
        printf '/d/kaixuan/llm-gateway\n'
      else
        printf '/c/llm-gateway\n'
      fi ;;
    *) printf '/opt/llm-gateway\n' ;;
  esac
}

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"
# 三个脚本（install-host/install-docker/upgrade）共用同一份 config。
# 这里允许 KXMAINT_CONFIG 指向文件覆盖，给测试或系统级部署留口子。
# load_config_file 会在文件不存在时直接返回，不自动创建；交互提示后
# 才调 save_config_file 写回。
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

# 交互式问一个值。stdin 不是 tty 或 NO_INTERACTIVE=1 时直接用 default。
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

# root 或 NO_SUDO=1（容器/测试）时直接执行；否则走 sudo。
run_priv() {
  if [[ "${NO_SUDO:-0}" == "1" || "$(id -u)" == "0" ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

load_config_file

# load_config_file 在文件顶部已调用；这里把"环境未设"的项用交互或 default 填齐。
# 写回发生在脚本末尾（save_config_file），不在此处——让前面的 normalize / validate
# 能直接基于最终生效值运行。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgo.kxpms.cn/maintain-api"
fi
MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
VERSION="${VERSION:-}"
# PLATFORM 默认按 uname 探测；llm-gateway-go 的 host 包支持 linux/amd64、linux/arm64、
# darwin/amd64、darwin/arm64（macOS 走裸机 launchd 路径）。loong64 需显式 LOONG64_OK=1。
PLATFORM="${PLATFORM:-$(uname -s | tr 'A-Z' 'a-z')}"
case "$PLATFORM" in
  linux|darwin) ;;
  windows)
    echo "[install-host] Windows host installation requires the PowerShell client-deploy.ps1 script" >&2
    exit 2
    ;;
  *) echo "[install-host] unsupported PLATFORM: $PLATFORM" >&2; exit 2 ;;
esac
# ARCH may be supplied explicitly by the generated command. Normalize catalog
# aliases while retaining uname detection when it is omitted.
# loong64 默认严格：未显式 LOONG64_OK=1 时直接退出（CI 默认不发 loong64 artifact）。
if [ -n "${ARCH:-}" ]; then
  case "$ARCH" in
    amd64|x86_64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    loong64|loongarch64) ARCH=loong64 ;;
    *) echo "[install-host] unsupported ARCH: $ARCH" >&2; exit 2 ;;
  esac
else
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    loongarch64|loong64) ARCH=loong64 ;;
    *) echo "unsupported arch: $arch" >&2; exit 2 ;;
  esac
fi
if [[ "$ARCH" == "loong64" && "${LOONG64_OK:-0}" != "1" ]]; then
  echo "[install-host] loongarch64 默认不启用（CI 默认不发 loong64 artifact）。如需安装请设 LOONG64_OK=1。" >&2
  exit 2
fi
# Validate VERSION (semver + build_seq; same character set as the UI's
# commandBuilder.ts allowlist). Reject anything that could break the
# downstream JSON payload to /downloads/ticket.
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[install-host] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi

INSTALL_ROOT="${INSTALL_ROOT:-$(detect_install_root)}"
EDITION="${EDITION:-customer}"
DRY_RUN="${DRY_RUN:-0}"

# 第一次成功生成默认值后，把可复用项写回 config —— 不写一次性值（VERSION/TARGET_VERSION）。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "$DRY_RUN" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL EDITION INSTALL_ROOT PLATFORM || true
}

verify_sha256() {
  local expected="$1" file="$2" actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    echo "[install-host] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  if [[ "${actual,,}" != "${expected,,}" ]]; then
    echo "[install-host] checksum MISMATCH $file" >&2
    return 1
  fi
}

catalog_sha() {
  local version="$1" platform="$2" want_arch="$3" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)" \
    || json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  LOOKUP_JSON="$json" WANT_VERSION="$version" WANT_PLATFORM="$platform" WANT_ARCH="$want_arch" python3 - <<'PY'
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

echo "[install-host] checking latest ${CHANNEL} for ${PLATFORM}/${ARCH}"
# If VERSION is set explicitly, skip version-check (which always returns "latest")
# and resolve the requested version directly via the ticket endpoint. This makes
# historical versions installable on demand. VERSION="" (default) preserves the
# old "latest on channel" path.
if [ -n "${VERSION:-}" ]; then
  TICKET_BODY="$(TV="${VERSION}" TP="${PLATFORM}" TA="${ARCH}" python3 -c '
import json, os
print(json.dumps({"version": os.environ["TV"], "platform": os.environ["TP"], "arch": os.environ["TA"]}))
')"
  TICKET_BODY="$(curl -fsSL \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "$TICKET_BODY" \
    "${MAINTAIN_BASE}/downloads/ticket")"
  URL="$(printf '%s' "${TICKET_BODY}" | sed -n 's/.*"url":"\([^\"]*\)".*/\1/p')"
  FILE="$(printf '%s' "${TICKET_BODY}" | sed -n 's/.*"file_name":"\([^\"]*\)".*/\1/p')"
  LATEST="${VERSION}"
  SHA="$(catalog_sha "${VERSION}" "${PLATFORM}" "${ARCH}" || true)"
  [[ -n "$SHA" ]] || {
    echo "[install-host] explicit VERSION=${VERSION} has no published sha256 for ${PLATFORM}/${ARCH} — refusing unverified download" >&2
    exit 1
  }
else
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=${PLATFORM}&arch=${ARCH}&edition=${EDITION}")"

  parse_check() {
    CHECK_JSON="$CHECK_JSON" python3 - <<'PY'
import json, os
d = json.loads(os.environ["CHECK_JSON"])
arts = d.get("target_artifacts") or []
art = arts[0] if arts else {}
print(d.get("latest_version") or "")
print(art.get("storage_uri") or "")
print(art.get("sha256") or "")
print(art.get("artifact_name") or art.get("filename") or "")
PY
  }

  mapfile -t _fields < <(parse_check)
  LATEST="${_fields[0]:-}"
  URL="${_fields[1]:-}"
  SHA="${_fields[2]:-}"
  FILE="${_fields[3]:-}"
fi

if [[ -z "$LATEST" || -z "$URL" ]]; then
  # If the user explicitly requested a VERSION, do NOT silently fall back to
  # the catalog top — they want what they asked for, or a clear error.
  if [ -n "${VERSION:-}" ]; then
    echo "[install-host] explicit VERSION=${VERSION} not found via ticket" >&2
    exit 1
  fi
  # fallback: ticket API after catalog
  VERSION="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" | python3 -c 'import sys,json; d=json.load(sys.stdin); vs=d.get("versions") or []; print(vs[0]["version"] if vs else "")')"
  [[ -n "$VERSION" ]] || { echo "no published release" >&2; exit 1; }
  TICKET_BODY="$(TV="$VERSION" TP="$PLATFORM" TA="$ARCH" python3 -c '
import json, os
print(json.dumps({"version": os.environ["TV"], "platform": os.environ["TP"], "arch": os.environ["TA"]}))
')"
  TICKET="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" -H 'Content-Type: application/json' \
    -d "$TICKET_BODY")"
  URL="$(printf '%s' "$TICKET" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("url",""))')"
  FILE="$(printf '%s' "$TICKET" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("file_name",""))')"
  LATEST="$VERSION"
fi

echo "[install-host] target=${LATEST} file=${FILE}"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
curl -fsSL "$URL" -o "${WORKDIR}/${FILE:-package.tar.gz}"
if [[ -n "$SHA" ]]; then
  verify_sha256 "$SHA" "${WORKDIR}/${FILE:-package.tar.gz}"
fi

if [[ "$DRY_RUN" == "1" ]]; then
  echo "[install-host] DRY_RUN=1 — downloaded only"
  exit 0
fi

sudo mkdir -p "$INSTALL_ROOT"
sudo tar -xzf "${WORKDIR}/${FILE:-package.tar.gz}" -C "$INSTALL_ROOT" --strip-components=1 2>/dev/null \
  || sudo tar -xzf "${WORKDIR}/${FILE:-package.tar.gz}" -C "$INSTALL_ROOT"
if [[ -x "$INSTALL_ROOT/install.sh" ]]; then
  sudo "$INSTALL_ROOT/install.sh"
elif [[ -x "$INSTALL_ROOT/scripts/install.sh" ]]; then
  sudo "$INSTALL_ROOT/scripts/install.sh"
fi
printf '%s\n' "$LATEST" | sudo tee "$INSTALL_ROOT/VERSION" >/dev/null

persist_config
echo "[install-host] done for ${PLATFORM}/${ARCH}. Activate at: ${MAINTAIN_BASE%/maintain-api}/maintain/activate"
echo "[install-host] offline: ${MAINTAIN_BASE%/maintain-api}/maintain/offline-activation"
