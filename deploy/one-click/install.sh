#!/usr/bin/env bash
# deploy/one-click/install.sh — 一键部署 PG + Redis + LLM Gateway（Linux / macOS）
#
# 用法:
#   bash deploy/one-click/install.sh                    # 交互安装 → ~/llm-gateway
#   bash deploy/one-click/install.sh --non-interactive  # 默认配置静默安装
#   bash deploy/one-click/install.sh --download v2.4.6 # 从下载站拉离线包后安装
#   LLM_GATEWAY_HOME=/opt/kx-gateway bash deploy/one-click/install.sh
#
# 等价快捷入口: bash deploy/one-click-install.sh
set -euo pipefail

OC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/bootstrap.sh
source "$OC_DIR/lib/bootstrap.sh"

REPO="$(repo_root)"
NON_INTERACTIVE=false
DOWNLOAD_VER=""
INSTALL_HOME="${LLM_GATEWAY_HOME:-$HOME/llm-gateway}"
EXTRA_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --non-interactive|--yes|-y) NON_INTERACTIVE=true; shift ;;
    --download) DOWNLOAD_VER="${2:-}"; shift 2 ;;
    --dir) INSTALL_HOME="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) EXTRA_ARGS+=("$1"); shift ;;
  esac
done

read -r OS ARCH < <(detect_platform)
WORKDIR="$REPO"
if [[ -n "$DOWNLOAD_VER" ]]; then
  DL_DIR="$(mktemp -d)"
  trap 'rm -rf "$DL_DIR"' EXIT
  download_offline_package "$DOWNLOAD_VER" "$OS" "$ARCH" "$DL_DIR"
  # 取解压后的第一个目录
  PKG_DIR="$(find "$DL_DIR" -maxdepth 1 -type d -name 'llm-gateway-go-*' | head -1)"
  [[ -n "$PKG_DIR" ]] && WORKDIR="$PKG_DIR"
fi

INSTALLER="$(resolve_installer "$WORKDIR" "$OS" "$ARCH")" || {
  echo "❌ 未找到 llm-gw-installer。请使用离线包或安装 Go 后重试。" >&2
  exit 1
}

sync_sql_baseline "$INSTALL_HOME"
mkdir -p "$INSTALL_HOME"

ARGS=(install --dir "$INSTALL_HOME")
if $NON_INTERACTIVE; then
  ARGS+=(--skip-prompt)
fi
ARGS+=("${EXTRA_ARGS[@]}")

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  LLM Gateway 一键部署（PostgreSQL + Redis + Gateway）         ║"
echo "╠══════════════════════════════════════════════════════════════╣"
echo "║  目标目录: $INSTALL_HOME"
echo "║  安装器:   $INSTALLER"
echo "╚══════════════════════════════════════════════════════════════╝"

exec "$INSTALLER" "${ARGS[@]}"
