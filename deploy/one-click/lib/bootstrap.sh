#!/usr/bin/env bash
# bootstrap.sh — 解析安装器、源码与 SQL 配置（deploy/one-click 公共库）
set -euo pipefail

one_click_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")" && pwd
}

repo_root() {
  local oc
  oc="$(one_click_root)"
  cd "$oc/../.." && pwd
}

detect_platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    loongarch64) arch="loong64" ;;
    *) echo "unsupported arch: $arch" >&2; return 1 ;;
  esac
  printf '%s %s\n' "$os" "$arch"
}

resolve_installer() {
  local root="$1" os="$2" arch="$3"
  local candidates=(
    "$root/llm-gw-installer-${os}-${arch}"
    "$root/bin/llm-gw-installer"
    "$root/llm-gw-installer"
    "$(repo_root)/dist/llm-gw-installer-${os}-${arch}"
  )
  local c
  for c in "${candidates[@]}"; do
    if [[ -f "$c" && -x "$c" ]]; then
      printf '%s\n' "$c"
      return 0
    fi
  done
  if command -v go >/dev/null 2>&1; then
    local out
    out="$(mktemp -d)/llm-gw-installer"
    echo "[one-click] building installer for ${os}/${arch}..." >&2
    (cd "$(repo_root)" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags="-s -w" -o "$out" ./installer/cmd/llm-gw-installer)
    chmod +x "$out"
    printf '%s\n' "$out"
    return 0
  fi
  return 1
}

sync_sql_baseline() {
  local dest="$1"
  local src
  src="$(repo_root)/deploy/sql/schemas/baseline"
  [[ -d "$src" ]] || return 0
  mkdir -p "$dest/sql/baseline"
  cp -a "$src/"*.sql "$dest/sql/baseline/" 2>/dev/null || true
}

download_offline_package() {
  local version="$1" os="$2" arch="$3" dest="$4"
  local base="${DOWNLOAD_BASE_URL:-https://download.kxpms.cn/llm-gateway-go}"
  local ver="${version#v}"
  local file="llm-gateway-go-${ver}-${os}-${arch}-offline.tar.gz"
  [[ "$os" == "windows" ]] && file="llm-gateway-go-${ver}-${os}-${arch}-offline.zip"
  local url="${base}/v${ver}/${file}"
  echo "[one-click] downloading $url" >&2
  mkdir -p "$dest"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 -o "$dest/$file" "$url"
  else
    wget -O "$dest/$file" "$url"
  fi
  if [[ "$file" == *.tar.gz ]]; then
    tar xzf "$dest/$file" -C "$dest"
  else
    command -v unzip >/dev/null 2>&1 || { echo "unzip required for windows package" >&2; return 1; }
    unzip -q "$dest/$file" -d "$dest"
  fi
}
