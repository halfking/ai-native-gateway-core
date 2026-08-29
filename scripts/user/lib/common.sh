#!/usr/bin/env bash
# scripts/user/lib/common.sh — 客户级安装脚本共享 utils（纯函数层）
#
# 所有 install-host.sh / install-docker.sh / upgrade.sh / client-deploy.sh 都 source 本文件。
# 保持每个用户脚本仍可独立 `curl -fsSL URL | bash` 调用（与 maintain 同协议）。
#
# 设计要点：
#   - 派生自 ai-native-maintain@main（2026-08-29）的 user/lib/，按 llm-gateway-go 服务身份调整。
#   - bash 3.2 兼容（macOS 系统 bash）：不用 declare -A / case ;; & 等 bash 4 特性。
#   - 仅放纯函数（无状态、无副作用）；KXMAINT_CONFIG / run_priv / prompt_value 等带状态工具
#     留在每个 user 脚本里 inline（与 maintain 上游一致），便于独立 curl pipe bash 调用。
#
# SOURCE_OF_TRUTH: /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/ai-native-maintain
#                  scripts/user/install-host.sh (commit ref to be filled)
# SYNC_POLICY: 修改本文件时同步改 maintain 对应位置；可被 env var 覆盖的值优先 env。
set -euo pipefail

# ── 默认值（与服务身份绑定；可被 env 覆盖） ──────────────────────────────
LLMG_DEFAULT_MAINTAIN_BASE="${LLMG_DEFAULT_MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
LLMG_DEFAULT_CHANNEL="${LLMG_DEFAULT_CHANNEL:-stable}"
LLMG_DEFAULT_EDITION="${LLMG_DEFAULT_EDITION:-customer}"
LLMG_DEFAULT_SERVICE_NAME="${LLMG_DEFAULT_SERVICE_NAME:-llm-gateway-go}"
LLMG_DEFAULT_SERVICE_USER="${LLMG_DEFAULT_SERVICE_USER:-llm-gateway}"
LLMG_DEFAULT_SERVICE_GROUP="${LLMG_DEFAULT_SERVICE_GROUP:-llm-gateway}"
LLMG_DEFAULT_BINARY_PREFIX="${LLMG_DEFAULT_BINARY_PREFIX:-llm-gateway-go}"
LLMG_DEFAULT_HEALTHZ_PORT="${LLMG_DEFAULT_HEALTHZ_PORT:-8080}"
LLMG_DEFAULT_PG_USER="${LLMG_DEFAULT_PG_USER:-llm_user}"
LLMG_DEFAULT_PG_DB="${LLMG_DEFAULT_PG_DB:-llm_gateway}"

# ── log/warn/die（纯函数；上游脚本用 log=xxx/warn=xxx 替换即可） ─────────
log()  { printf '[llmg-install] %s\n' "$*"; }
warn() { printf '[llmg-install] WARN: %s\n' "$*" >&2; }
die()  { printf '[llmg-install] ERROR: %s\n' "$*" >&2; exit 1; }

# ── 平台探测 ──────────────────────────────────────────────────────────────
# 输出: linux / darwin / windows
detect_platform() {
  local k
  k="$(uname -s 2>/dev/null || echo Unknown)"
  case "$k" in
    Linux*)  printf 'linux\n' ;;
    Darwin*) printf 'darwin\n' ;;
    MINGW*|MSYS*|CYGWIN*) printf 'windows\n' ;;
    *) die "unsupported platform: $k (请在 WSL/Linux/macOS 上运行；Windows 用 client-deploy.ps1)" ;;
  esac
}

# ── CPU 架构探测 ──────────────────────────────────────────────────────────
# 输出: amd64 / arm64 / loong64
# loong64 默认严格：未显式 LOONG64_OK=1 时直接退出。
#   严格原因：CI 默认不发 loong64 artifact（package.sh --include-loong64 才发）；
#   静默放行会导致用户拿到一个空的 /downloads/ticket 然后报错难以诊断。
detect_arch() {
  local raw arch="${ARCH:-}"
  if [[ -z "$arch" ]]; then
    raw="$(uname -m)"
    case "$raw" in
      x86_64|amd64)         arch=amd64 ;;
      aarch64|arm64)        arch=arm64 ;;
      loongarch64|loong64)  arch=loong64 ;;
      *) die "unsupported CPU arch: $raw (支持 amd64/arm64/loong64)" ;;
    esac
  fi
  case "$arch" in
    amd64|arm64) printf '%s\n' "$arch" ;;
    loong64)
      if [[ "${LOONG64_OK:-0}" != "1" ]]; then
        die "loongarch64 默认不启用（CI 默认不发 loong64 artifact）。如需安装请设 LOONG64_OK=1 并确认发布方已发对应 platform=linux/arch=loong64 的 artifact。"
      fi
      printf '%s\n' "$arch" ;;
    *) die "invalid ARCH=$arch (支持 amd64/arm64/loong64)" ;;
  esac
}

# ── INSTALL_ROOT 智能探测 ────────────────────────────────────────────────
# 决策矩阵（按用户原话"macOS=~/kaixuan/, linux=/opt/, windows=D:kaixuan/"实现）：
#   macOS  : ~/Downloads/llm-gateway-files（与 local-host-deploy.sh 一致）
#            └─ ~/Downloads 不可写 → ~/.local/llm-gateway
#   Linux  : /opt/llm-gateway
#            └─ /opt 不可写 → ~/.local/llm-gateway
#   Windows: /d/kaixuan/llm-gateway (D:/kaixuan/llm-gateway)
#            └─ /d 不存在或不可写 → /c/llm-gateway (C:/llm-gateway)
# 环境变量 INSTALL_ROOT 永远最高优先级。
detect_install_root() {
  if [[ -n "${INSTALL_ROOT:-}" ]]; then
    printf '%s\n' "$INSTALL_ROOT"
    return 0
  fi
  local p
  p="$(detect_platform)"
  case "$p" in
    darwin)
      if [[ -w "$HOME/Downloads" ]]; then
        printf '%s\n' "$HOME/Downloads/llm-gateway-files"
      else
        printf '%s\n' "$HOME/.local/llm-gateway"
      fi ;;
    linux)
      if [[ -w /opt ]]; then
        printf '/opt/llm-gateway\n'
      else
        printf '%s\n' "$HOME/.local/llm-gateway"
      fi ;;
    windows)
      # /d = D:, /c = C: (Git Bash / MSYS / Cygwin 约定)
      if [[ -d /d && -w /d ]]; then
        printf '/d/kaixuan/llm-gateway\n'
      else
        printf '/c/llm-gateway\n'
      fi ;;
  esac
}

# ── 校验/计算 sha256 ─────────────────────────────────────────────────────
sha256_of() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    die "no sha256 tool (sha256sum/shasum)"
  fi
}

verify_sha256() {
  local expected="$1" file="$2" actual
  if ! actual="$(sha256_of "$file")"; then
    return 1
  fi
  # 大小写不敏感比较（catalog 给的可能全小写，shasum 输出小写，sha256sum 兼容）
  local e_low a_low
  e_low="$(printf '%s' "$expected" | tr 'A-Z' 'a-z')"
  a_low="$(printf '%s' "$actual" | tr 'A-Z' 'a-z')"
  if [[ "$e_low" != "$a_low" ]]; then
    die "checksum MISMATCH $file (expected=$e_low, got=$a_low)"
  fi
  log "sha256 OK $(basename "$file")"
}

# ── catalog/ticket 接口 ──────────────────────────────────────────────────
# 解析 catalog JSON，取 (version, platform, arch) 对应的 sha256。
catalog_sha() {
  local version="$1" platform="$2" want_arch="$3" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)" \
    || json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || { printf '\n'; return 0; }
  LOOKUP_JSON="$json" WANT_VERSION="$version" WANT_PLATFORM="$platform" WANT_ARCH="$want_arch" python3 - <<'PY'
import json, os
try:
    d = json.loads(os.environ["LOOKUP_JSON"])
except Exception:
    raise SystemExit(0)
want_v = os.environ["WANT_VERSION"].lstrip("v")
want_p = os.environ["WANT_PLATFORM"]
want_a = os.environ["WANT_ARCH"]
items = d.get("items") or d.get("versions") or []
for v in items:
    if not isinstance(v, dict):
        continue
    if (v.get("version") or "").lstrip("v") != want_v:
        continue
    arts = v.get("items") or v.get("artifacts") or []
    for a in arts:
        if isinstance(a, dict) and a.get("platform") == want_p and a.get("arch") == want_a:
            print(a.get("sha256") or "")
            raise SystemExit(0)
PY
}

# ticket 端点：拿到 signed download url + file_name（stdin 接收 JSON response）。
# 调用方：`URL=$(ticket_url ... )` 提取 url 字段。
ticket_body() {
  local version="$1" platform="$2" arch="$3"
  TV="$version" TP="$platform" TA="$arch" python3 -c '
import json, os
print(json.dumps({"version": os.environ["TV"], "platform": os.environ["TP"], "arch": os.environ["TA"]}))'
}

# ── version-check：返回 latest + signed url + sha + filename ─────────────
version_check() {
  local current="$1" platform="$2" arch="$3" edition="$4"
  curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=${current}&platform=${platform}&arch=${arch}&edition=${edition}"
}

# ── validate version 字符串（与 commandBuilder.ts allowlist 对齐） ────────
validate_version_string() {
  local v="$1"
  [[ -z "$v" ]] && return 0  # 空表示 latest，OK
  [[ "$v" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    die "invalid VERSION=$v (must match ^[0-9A-Za-z._+-]{1,64}\$)"
  }
}

# 本脚本 source 时静默；所有副作用都在函数调用时发生。
true
