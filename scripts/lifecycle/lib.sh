#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/lifecycle/lib.sh
# SYNC_POLICY: 修改本文件时同步改 maintain 对应位置。service identity 由调用方传 env (BINARY_PREFIX / SERVICE_NAME)。
# ADAPTATIONS: service_platform 加 windows；release_binary_name 接受 BINARY_PREFIX (默认 llm-gateway-go)；删 loong64 die。

set -euo pipefail

LIFECYCLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC2034 # referenced by sibling lifecycle scripts when sourced
PROJECT_ROOT="$(cd "$LIFECYCLE_DIR/../.." && pwd)"

log() { printf '[lifecycle] %s\n' "$*"; }
warn() { printf '[lifecycle] WARNING: %s\n' "$*" >&2; }
die() { printf '[lifecycle] ERROR: %s\n' "$*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

require_value() {
  local name=$1 value=${2:-}
  [[ -n "$value" ]] || die "$name is required"
}

confirm() {
  local expected=$1 message=$2 supplied=${3:-${CONFIRM:-}}
  if [[ "$supplied" == "$expected" ]]; then
    return 0
  fi
  if [[ -t 0 ]]; then
    printf '%s\nType %s to continue: ' "$message" "$expected" >&2
    IFS= read -r supplied
    [[ "$supplied" == "$expected" ]] || die "confirmation did not match"
    return 0
  fi
  die "non-interactive operation requires --confirm $expected or CONFIRM=$expected"
}

sha256_file() {
  local file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | cut -d ' ' -f 1
  else
    die "sha256sum or shasum is required"
  fi
}

file_size() {
  local file=$1
  if stat -f '%z' "$file" >/dev/null 2>&1; then
    stat -f '%z' "$file"
  else
    stat -c '%s' "$file"
  fi
}

json_string() {
  python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"
}

absolute_path() {
  python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "$1"
}

manifest_value() {
  local manifest=$1 key=$2
  python3 - "$manifest" "$key" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
value = data
for part in sys.argv[2].split("."):
    value = value[part]
print(value)
PY
}

validate_backup() {
  local backup=$1
  # shellcheck disable=SC2318 # manifest var is intentional — first in a chain of two `local`s below
  local manifest=${2:-"$backup.manifest.json"}
  [[ -f "$backup" ]] || die "backup file not found: $backup"
  [[ -f "$manifest" ]] || die "backup manifest not found: $manifest"
  need_cmd python3
  need_cmd pg_restore

  local expected actual manifest_file
  manifest_file=$(manifest_value "$manifest" file)
  [[ "$manifest_file" == "$(basename "$backup")" ]] || die "manifest file does not match backup: $manifest_file"
  expected=$(manifest_value "$manifest" sha256)
  actual=$(sha256_file "$backup")
  [[ "$expected" == "$actual" ]] || die "backup checksum mismatch: expected $expected, got $actual"
  pg_restore --list "$backup" >/dev/null || die "pg_restore could not read backup catalog"
}

service_platform() {
  case "$(uname -s)" in
    Linux) printf 'linux\n' ;;
    Darwin) printf 'darwin\n' ;;
    MINGW*|MSYS*|CYGWIN*) printf 'windows\n' ;;
    *) die "unsupported service platform: $(uname -s); Windows users should use lifecycle.ps1 from install-service.ps1" ;;
  esac
}

# release_binary_name <platform>  →  <prefix>-<platform>-<arch>
# BINARY_PREFIX env 默认 llm-gateway-go（与本仓库二进制命名一致）。
# maintain 调用时设 BINARY_PREFIX=maintain 即可复用同一函数。
release_binary_name() {
  local platform=$1 arch prefix=${BINARY_PREFIX:-llm-gateway-go}
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    loongarch64|loong64) arch=loong64 ;;
    *) die "unsupported host architecture: $arch" ;;
  esac
  printf '%s-%s-%s\n' "$prefix" "$platform" "$arch"
}
